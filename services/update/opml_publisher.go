package update

import (
	"context"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

type OPMLBuildFunc func(ctx context.Context) error

type OPMLPublisher struct {
	build    OPMLBuildFunc
	debounce time.Duration

	mu      sync.Mutex
	timer   *time.Timer
	pending bool
}

func NewOPMLPublisher(build OPMLBuildFunc, debounce time.Duration) *OPMLPublisher {
	if debounce <= 0 {
		debounce = time.Second
	}
	return &OPMLPublisher{build: build, debounce: debounce}
}

func (p *OPMLPublisher) Request(_ context.Context) {
	if p == nil || p.build == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = true
	if p.timer != nil {
		p.timer.Reset(p.debounce)
		return
	}
	p.timer = time.AfterFunc(p.debounce, func() {
		_ = p.flush(context.Background())
	})
	log.WithField("debounce", p.debounce).Debug("opml rebuild requested")
}

func (p *OPMLPublisher) Flush(ctx context.Context) error {
	if p == nil || p.build == nil {
		return nil
	}
	return p.flush(ctx)
}

func (p *OPMLPublisher) flush(ctx context.Context) error {
	p.mu.Lock()
	if !p.pending {
		p.mu.Unlock()
		return nil
	}
	p.pending = false
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.mu.Unlock()

	log.Debug("building podcast OPML from publisher")
	return p.build(ctx)
}
