package fs

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

// LocalConfig is the storage configuration for local file system
type LocalConfig struct {
	DataDir string `toml:"data_dir"`
}

// Local implements local file storage
type Local struct {
	rootDir      string
	WebUIEnabled bool
}

// RootDir returns the configured root directory for local storage.
// Inputs: none.
// Outputs: root directory path.
// Example usage:
//
//	root := local.RootDir()
//
// Notes: Used to derive related paths (e.g., signatures directory).
func (l *Local) RootDir() string {
	return l.rootDir
}

func NewLocal(rootDir string, webUIEnabled bool) (*Local, error) {
	return &Local{rootDir: rootDir, WebUIEnabled: webUIEnabled}, nil
}

func (l *Local) Open(name string) (http.File, error) {
	if name == "/index.html" && l.WebUIEnabled {
		return os.Open("./html/index.html")
	}
	path := filepath.Join(l.rootDir, name)
	return os.Open(path)
}

func (l *Local) Delete(_ctx context.Context, name string) error {
	path := filepath.Join(l.rootDir, name)
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("failed to delete file %s: %w", path, err)
	}
	return nil
}

func (l *Local) Create(_ctx context.Context, name string, reader io.Reader) (int64, error) {
	session, err := l.BeginPublish(context.Background(), name)
	if err != nil {
		return 0, err
	}
	defer session.Abort()
	if _, err := io.Copy(session, reader); err != nil {
		return 0, errors.Wrap(err, "failed to stage file")
	}
	return session.Commit(context.Background())
}

func (l *Local) BeginPublish(_ctx context.Context, name string) (PublishSession, error) {
	path := filepath.Join(l.rootDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, errors.Wrapf(err, "failed to mkdir: %s", path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create temporary destination file")
	}
	return &localPublishSession{file: tmp, tmpPath: tmp.Name(), finalPath: path}, nil
}

func (l *Local) copyFile(source io.Reader, destinationPath string) (int64, error) {
	dir := filepath.Dir(destinationPath)
	tmp, err := os.CreateTemp(dir, filepath.Base(destinationPath)+".tmp-*")
	if err != nil {
		return 0, errors.Wrap(err, "failed to create temporary destination file")
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	written, err := io.Copy(tmp, source)
	if err != nil {
		return 0, errors.Wrap(err, "failed to copy data")
	}
	if err := tmp.Sync(); err != nil {
		return 0, errors.Wrap(err, "failed to sync temporary file")
	}
	if err := tmp.Close(); err != nil {
		return 0, errors.Wrap(err, "failed to close temporary file")
	}
	if err := os.Rename(tmpPath, destinationPath); err != nil {
		return 0, errors.Wrap(err, "failed to publish temporary file")
	}

	return written, nil
}

type localPublishSession struct {
	file      *os.File
	tmpPath   string
	finalPath string
	closed    bool
}

func (s *localPublishSession) Write(p []byte) (int, error) { return s.file.Write(p) }

func (s *localPublishSession) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.file.Close()
}

func (s *localPublishSession) Commit(_ctx context.Context) (int64, error) {
	if err := s.file.Sync(); err != nil {
		return 0, errors.Wrap(err, "failed to sync temporary file")
	}
	info, err := s.file.Stat()
	if err != nil {
		return 0, errors.Wrap(err, "failed to stat temporary file")
	}
	if err := s.Close(); err != nil {
		return 0, errors.Wrap(err, "failed to close temporary file")
	}
	if err := os.Rename(s.tmpPath, s.finalPath); err != nil {
		return 0, errors.Wrap(err, "failed to publish temporary file")
	}
	return info.Size(), nil
}

func (s *localPublishSession) Abort() error {
	_ = s.Close()
	if err := os.Remove(s.tmpPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *Local) Size(_ctx context.Context, name string) (int64, error) {
	file, err := l.Open(name)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return 0, err
	}

	return stat.Size(), nil
}
