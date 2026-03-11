package update

import (
	"context"
	"expvar"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mxpv/podsync/pkg/builder"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
	log "github.com/sirupsen/logrus"
)

var (
	metricQueueDepth      = expvar.NewInt("update_queue_depth")
	metricActiveFeeds     = expvar.NewInt("update_active_feeds")
	metricEnqueueRequests = expvar.NewInt("update_enqueue_requests_total")
	metricEnqueueDropped  = expvar.NewInt("update_enqueue_deduplicated_total")
	metricRunsStarted     = expvar.NewInt("update_runs_started_total")
	metricRunsFinished    = expvar.NewInt("update_runs_finished_total")
	metricRunsFailed      = expvar.NewInt("update_runs_failed_total")
	metricProviderBlocked = expvar.NewInt("update_provider_limit_waits_total")
)

type FeedUpdater interface {
	Update(ctx context.Context, feedConfig *feed.Config) error
}

type Scheduler struct {
	updater FeedUpdater
	workers int

	queue chan *feed.Config

	mu          sync.Mutex
	enqueued    map[string]*feed.Config
	inFlight    map[string]struct{}
	stopping    bool
	activeRuns  int32
	wg          sync.WaitGroup
	providerSem map[model.Provider]chan struct{}
}

func NewScheduler(updater FeedUpdater, workers int, queueSize int) *Scheduler {
	if workers < 1 {
		workers = 1
	}
	if queueSize < workers {
		queueSize = workers
	}
	return &Scheduler{
		updater:  updater,
		workers:  workers,
		queue:    make(chan *feed.Config, queueSize),
		enqueued: map[string]*feed.Config{},
		inFlight: map[string]struct{}{},
		providerSem: map[model.Provider]chan struct{}{
			model.ProviderRumble: make(chan struct{}, 1),
		},
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx, i+1)
	}
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.stopping {
		s.stopping = true
		close(s.queue)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Scheduler) Enqueue(cfg *feed.Config) bool {
	if cfg == nil {
		return false
	}
	metricEnqueueRequests.Add(1)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stopping {
		return false
	}
	if _, ok := s.inFlight[cfg.ID]; ok {
		metricEnqueueDropped.Add(1)
		log.WithField("feed_id", cfg.ID).Debug("skipping update enqueue because feed is already running")
		return false
	}
	if _, ok := s.enqueued[cfg.ID]; ok {
		metricEnqueueDropped.Add(1)
		log.WithField("feed_id", cfg.ID).Debug("skipping update enqueue because feed is already queued")
		return false
	}

	s.enqueued[cfg.ID] = cfg
	metricQueueDepth.Set(int64(len(s.enqueued)))
	log.WithFields(log.Fields{
		"feed_id":     cfg.ID,
		"queue_depth": len(s.enqueued),
	}).Info("feed update enqueued")
	s.queue <- cfg
	return true
}

func (s *Scheduler) worker(ctx context.Context, workerID int) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case cfg, ok := <-s.queue:
			if !ok {
				return
			}
			if cfg == nil {
				continue
			}
			s.startFeed(cfg)
			started := time.Now()
			runCtx, executionID := withExecutionID(ctx, cfg.ID)
			releaseProvider := s.acquireProviderSlot(cfg, executionID)
			metricRunsStarted.Add(1)
			metricActiveFeeds.Set(int64(atomic.AddInt32(&s.activeRuns, 1)))
			logger := log.WithFields(log.Fields{
				"feed_id":      cfg.ID,
				"worker_id":    workerID,
				"queue_depth":  s.QueueDepth(),
				"active_feeds": atomic.LoadInt32(&s.activeRuns),
				"execution_id": executionID,
			})
			logger.Info("feed update started")
			err := s.updater.Update(runCtx, cfg)
			duration := time.Since(started)
			if err != nil {
				metricRunsFailed.Add(1)
				logger.WithError(err).WithField("duration", duration).Error("feed update failed")
			} else {
				logger.WithField("duration", duration).Info("feed update finished")
			}
			metricRunsFinished.Add(1)
			metricActiveFeeds.Set(int64(atomic.AddInt32(&s.activeRuns, -1)))
			if releaseProvider != nil {
				releaseProvider()
			}
			s.finishFeed(cfg)
		}
	}
}

func (s *Scheduler) acquireProviderSlot(cfg *feed.Config, executionID string) func() {
	provider, ok := s.providerForFeed(cfg)
	if !ok {
		return nil
	}
	sem, ok := s.providerSem[provider]
	if !ok {
		return nil
	}
	if len(sem) == cap(sem) {
		metricProviderBlocked.Add(1)
		log.WithFields(log.Fields{"feed_id": cfg.ID, "provider": provider, "execution_id": executionID}).Info("waiting for provider execution slot")
	}
	sem <- struct{}{}
	log.WithFields(log.Fields{"feed_id": cfg.ID, "provider": provider, "execution_id": executionID}).Debug("provider execution slot acquired")
	return func() {
		<-sem
		log.WithFields(log.Fields{"feed_id": cfg.ID, "provider": provider, "execution_id": executionID}).Debug("provider execution slot released")
	}
}

func (s *Scheduler) providerForFeed(cfg *feed.Config) (model.Provider, bool) {
	if cfg == nil || cfg.URL == "" {
		return "", false
	}
	info, err := builder.ParseURL(cfg.URL)
	if err != nil {
		return "", false
	}
	return info.Provider, true
}

func (s *Scheduler) startFeed(cfg *feed.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.enqueued, cfg.ID)
	s.inFlight[cfg.ID] = struct{}{}
	metricQueueDepth.Set(int64(len(s.enqueued)))
}

func (s *Scheduler) finishFeed(cfg *feed.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inFlight, cfg.ID)
	metricQueueDepth.Set(int64(len(s.enqueued)))
}

func (s *Scheduler) QueueDepth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.enqueued)
}

func (s *Scheduler) InFlight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inFlight)
}

func (s *Scheduler) Stats() string {
	return fmt.Sprintf("queued=%d in_flight=%d", s.QueueDepth(), s.InFlight())
}
