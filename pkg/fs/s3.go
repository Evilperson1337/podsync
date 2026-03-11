package fs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/gabriel-vasile/mimetype"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// S3Config is the configuration for a S3-compatible storage provider
type S3Config struct {
	// S3 Bucket to store files
	Bucket string `toml:"bucket"`
	// Region of the S3 service
	Region string `toml:"region"`
	// EndpointURL is an HTTP endpoint of the S3 API
	EndpointURL string `toml:"endpoint_url"`
	// Prefix is a prefix (subfolder) to use to build key names
	Prefix string `toml:"prefix"`
}

// S3 implements file storage for S3-compatible providers.
type S3 struct {
	api      s3iface.S3API
	uploader *s3manager.Uploader
	bucket   string
	prefix   string
}

func NewS3(c S3Config) (*S3, error) {
	cfg := aws.NewConfig().
		WithEndpoint(c.EndpointURL).
		WithRegion(c.Region).
		WithLogger(s3logger{}).
		WithLogLevel(aws.LogDebug)
	sess, err := session.NewSessionWithOptions(session.Options{Config: *cfg})
	if err != nil {
		return nil, errors.Wrap(err, "failed to initialize S3 session")
	}
	return &S3{
		api:      s3.New(sess),
		uploader: s3manager.NewUploader(sess),
		bucket:   c.Bucket,
		prefix:   c.Prefix,
	}, nil
}

func (s *S3) Open(_name string) (http.File, error) {
	return nil, errors.New("serving files from S3 is not supported")
}

func (s *S3) Delete(ctx context.Context, name string) error {
	key := s.buildKey(name)
	_, err := s.api.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			if awsErr.Code() == "NotFound" {
				return os.ErrNotExist
			}
		}
	}
	return err
}

func (s *S3) Create(ctx context.Context, name string, reader io.Reader) (int64, error) {
	session, err := s.BeginPublish(ctx, name)
	if err != nil {
		return 0, err
	}
	defer session.Abort()
	if _, err := io.Copy(session, reader); err != nil {
		return 0, errors.Wrap(err, "failed to stage file")
	}
	return session.Commit(ctx)
}

func (s *S3) BeginPublish(_ctx context.Context, name string) (PublishSession, error) {
	tmp, err := os.CreateTemp("", "podsync-s3-publish-*")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create publish staging file")
	}
	return &s3PublishSession{storage: s, file: tmp, name: name, tmpPath: tmp.Name()}, nil
}

func (s *S3) Size(ctx context.Context, name string) (int64, error) {
	key := s.buildKey(name)
	logger := log.WithField("key", key)

	logger.Debugf("getting file size from %s", s.bucket)
	resp, err := s.api.HeadObjectWithContext(ctx, &s3.HeadObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})
	if err != nil {
		if awsErr, ok := err.(awserr.Error); ok {
			if awsErr.Code() == "NotFound" {
				return 0, os.ErrNotExist
			}
		}
		return 0, errors.Wrap(err, "failed to get file size")
	}

	return *resp.ContentLength, nil
}

func (s *S3) buildKey(name string) string {
	return path.Join(s.prefix, name)
}

func (s *S3) tempKey(name string) string {
	return fmt.Sprintf("%s.tmp-%d", s.buildKey(name), time.Now().UTC().UnixNano())
}

type readerWithN struct {
	io.Reader
	n int
}

type s3PublishSession struct {
	storage *S3
	file    *os.File
	name    string
	tmpPath string
	closed  bool
}

func (s *s3PublishSession) Write(p []byte) (int, error) { return s.file.Write(p) }

func (s *s3PublishSession) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.file.Close()
}

func (s *s3PublishSession) Commit(ctx context.Context) (int64, error) {
	if err := s.Close(); err != nil {
		return 0, err
	}
	file, err := os.Open(s.tmpPath)
	if err != nil {
		return 0, errors.Wrap(err, "failed to open staged publish file")
	}
	defer file.Close()

	key := s.storage.buildKey(s.name)
	tempKey := s.storage.tempKey(s.name)
	logger := log.WithFields(log.Fields{"key": key, "temp_key": tempKey})

	buf := make([]byte, 512)
	n, err := io.ReadFull(file, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return 0, errors.Wrap(err, "failed to read file header for MIME detection")
	}
	head := buf[:n]
	m := mimetype.Detect(head)
	body := io.MultiReader(bytes.NewReader(head), file)
	info, err := os.Stat(s.tmpPath)
	if err != nil {
		return 0, errors.Wrap(err, "failed to stat staged publish file")
	}

	logger.Infof("uploading staged file to %s", s.storage.bucket)
	r := &readerWithN{Reader: body}
	_, err = s.storage.uploader.UploadWithContext(ctx, &s3manager.UploadInput{
		Body:        r,
		Bucket:      &s.storage.bucket,
		ContentType: aws.String(m.String()),
		Key:         &tempKey,
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to upload file")
	}
	_, err = s.storage.api.CopyObjectWithContext(ctx, &s3.CopyObjectInput{
		Bucket:      &s.storage.bucket,
		CopySource:  aws.String(path.Join(s.storage.bucket, tempKey)),
		ContentType: aws.String(m.String()),
		Key:         &key,
	})
	if err != nil {
		_, _ = s.storage.api.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{Bucket: &s.storage.bucket, Key: &tempKey})
		return 0, errors.Wrap(err, "failed to publish staged file")
	}
	_, _ = s.storage.api.DeleteObjectWithContext(ctx, &s3.DeleteObjectInput{Bucket: &s.storage.bucket, Key: &tempKey})
	return info.Size(), nil
}

func (s *s3PublishSession) Abort() error {
	_ = s.Close()
	if err := os.Remove(s.tmpPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (r *readerWithN) Read(p []byte) (n int, err error) {
	n, err = r.Reader.Read(p)
	r.n += n
	return
}

type s3logger struct{}

func (s s3logger) Log(args ...interface{}) {
	log.Debug(args...)
}
