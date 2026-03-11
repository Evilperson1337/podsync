package update

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
)

type schedulerTestUpdater struct {
	mu       sync.Mutex
	active   map[string]int
	maxTotal int
	total    int
	order    []string
	entered  chan string
	release  chan struct{}
	execIDs  []string
}

func (u *schedulerTestUpdater) Update(ctx context.Context, cfg *feed.Config) error {
	u.mu.Lock()
	if u.active == nil {
		u.active = map[string]int{}
	}
	u.active[cfg.ID]++
	u.total++
	if u.total > u.maxTotal {
		u.maxTotal = u.total
	}
	current := u.active[cfg.ID]
	u.order = append(u.order, cfg.ID)
	u.execIDs = append(u.execIDs, executionIDFromContext(ctx))
	u.mu.Unlock()

	if current > 1 {
		panic("same feed executed concurrently")
	}

	if u.entered != nil {
		u.entered <- cfg.ID
	}
	if u.release != nil {
		<-u.release
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	u.active[cfg.ID]--
	u.total--
	return nil
}

func TestSchedulerDeduplicatesQueuedAndRunningFeeds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updater := &schedulerTestUpdater{entered: make(chan string, 4), release: make(chan struct{})}
	scheduler := NewScheduler(updater, 2, 4)
	scheduler.Start(ctx)
	t.Cleanup(scheduler.Stop)

	require.True(t, scheduler.Enqueue(&feed.Config{ID: "feed-a"}))
	require.False(t, scheduler.Enqueue(&feed.Config{ID: "feed-a"}))
	<-updater.entered
	require.False(t, scheduler.Enqueue(&feed.Config{ID: "feed-a"}))

	updater.release <- struct{}{}
	time.Sleep(20 * time.Millisecond)
	require.True(t, scheduler.Enqueue(&feed.Config{ID: "feed-a"}))
	updater.release <- struct{}{}
	time.Sleep(20 * time.Millisecond)

	assert.Equal(t, 0, scheduler.QueueDepth())
	assert.Equal(t, 0, scheduler.InFlight())
	assert.Equal(t, []string{"feed-a", "feed-a"}, updater.order)
}

func TestSchedulerRunsDifferentFeedsConcurrently(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updater := &schedulerTestUpdater{entered: make(chan string, 4), release: make(chan struct{})}
	scheduler := NewScheduler(updater, 2, 4)
	scheduler.Start(ctx)
	t.Cleanup(scheduler.Stop)

	require.True(t, scheduler.Enqueue(&feed.Config{ID: "feed-a"}))
	require.True(t, scheduler.Enqueue(&feed.Config{ID: "feed-b"}))

	first := <-updater.entered
	second := <-updater.entered
	assert.ElementsMatch(t, []string{"feed-a", "feed-b"}, []string{first, second})
	assert.Equal(t, 2, updater.maxTotal)

	updater.release <- struct{}{}
	updater.release <- struct{}{}
	time.Sleep(20 * time.Millisecond)

	assert.Equal(t, 0, scheduler.InFlight())
}

func TestSchedulerAppliesProviderScopedLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updater := &schedulerTestUpdater{entered: make(chan string, 4), release: make(chan struct{})}
	scheduler := NewScheduler(updater, 2, 4)
	scheduler.Start(ctx)
	t.Cleanup(scheduler.Stop)

	require.True(t, scheduler.Enqueue(&feed.Config{ID: "feed-a", URL: "https://rumble.com/c/A"}))
	require.True(t, scheduler.Enqueue(&feed.Config{ID: "feed-b", URL: "https://rumble.com/c/B"}))

	first := <-updater.entered
	assert.Contains(t, []string{"feed-a", "feed-b"}, first)

	select {
	case <-updater.entered:
		t.Fatal("second rumble feed should wait for provider slot")
	case <-time.After(30 * time.Millisecond):
	}

	updater.release <- struct{}{}
	second := <-updater.entered
	assert.Contains(t, []string{"feed-a", "feed-b"}, second)
	updater.release <- struct{}{}
	assert.LessOrEqual(t, updater.maxTotal, 1)
	for _, executionID := range updater.execIDs {
		assert.NotEmpty(t, executionID)
	}
}
