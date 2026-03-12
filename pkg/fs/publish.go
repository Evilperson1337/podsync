package fs

import (
	"context"
	"fmt"
	"io"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type PublishOptions struct {
	MinSize int64
}

type PublishResult struct {
	Size int64
}

type Publisher struct {
	storage Storage
}

func NewPublisher(storage Storage) *Publisher {
	return &Publisher{storage: storage}
}

func (p *Publisher) Publish(ctx context.Context, name string, reader io.Reader, opts PublishOptions) (*PublishResult, error) {
	if p == nil || p.storage == nil {
		return nil, fmt.Errorf("publisher storage is nil")
	}
	session, err := p.storage.BeginPublish(ctx, name)
	if err != nil {
		return nil, err
	}
	defer session.Abort()

	stagedBytes, err := io.Copy(session, reader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to stage publish content")
	}
	if opts.MinSize > 0 && stagedBytes < opts.MinSize {
		return nil, fmt.Errorf("staged content smaller than required minimum: %d < %d", stagedBytes, opts.MinSize)
	}

	log.WithFields(log.Fields{"name": name, "staged_bytes": stagedBytes, "min_size": opts.MinSize}).Debug("publishing staged content")
	written, err := session.Commit(ctx)
	if err != nil {
		return nil, err
	}
	if opts.MinSize > 0 && written < opts.MinSize {
		return nil, fmt.Errorf("published content smaller than required minimum: %d < %d", written, opts.MinSize)
	}
	return &PublishResult{Size: written}, nil
}
