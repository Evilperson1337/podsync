package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

type fakeCron struct {
	next    cron.EntryID
	entries map[cron.EntryID]string
	removed []cron.EntryID
}

func newFakeCron() *fakeCron { return &fakeCron{entries: map[cron.EntryID]string{}} }

func (f *fakeCron) AddFunc(spec string, _ func()) (cron.EntryID, error) {
	if _, err := cron.ParseStandard(spec); err != nil {
		return 0, err
	}
	f.next++
	f.entries[f.next] = spec
	return f.next, nil
}

func (f *fakeCron) Entry(id cron.EntryID) cron.Entry {
	if _, ok := f.entries[id]; !ok {
		return cron.Entry{}
	}
	return cron.Entry{ID: id, Next: time.Date(2030, 1, 1, 0, 0, 0, int(id), time.UTC)}
}

func (f *fakeCron) Remove(id cron.EntryID) {
	delete(f.entries, id)
	f.removed = append(f.removed, id)
}

type fakeQueue struct{ enqueued []string }

func (q *fakeQueue) Enqueue(cfg *feed.Config) bool {
	q.enqueued = append(q.enqueued, cfg.ID)
	return true
}

type fakeFeedUpdater struct {
	mu    sync.Mutex
	feeds map[string]*feed.Config
	keys  map[model.Provider]feed.KeyProvider
	calls int
}

func (u *fakeFeedUpdater) SetFeeds(feeds map[string]*feed.Config) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.feeds = feeds
	u.calls++
}

func (u *fakeFeedUpdater) SetKeys(keys map[model.Provider]feed.KeyProvider) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.keys = keys
}

func feedsOf(configs ...*feed.Config) map[string]*feed.Config {
	feeds := map[string]*feed.Config{}
	for _, cfg := range configs {
		feeds[cfg.ID] = cfg
	}
	return feeds
}

func TestFeedScheduleApply(t *testing.T) {
	c, queue := newFakeCron(), &fakeQueue{}
	schedule := newFeedSchedule(c, queue, false)

	periodic := &feed.Config{ID: "periodic", URL: "a", UpdatePeriod: 6 * time.Hour}
	fixed := &feed.Config{ID: "fixed", URL: "b", CronSchedule: "0 3 * * *"}
	changes, err := schedule.Apply(feedsOf(periodic, fixed))
	require.NoError(t, err)
	assert.Equal(t, []string{"fixed", "periodic"}, changes.Added)
	assert.Equal(t, []string{"periodic"}, queue.enqueued, "feeds with an explicit cron_schedule wait for it")
	assert.ElementsMatch(t, []string{"@every 6h0m0s", "0 3 * * *"}, valuesOf(c.entries))
	assert.Equal(t, time.Duration(0), fixed.UpdatePeriod, "configs are not modified")
	assert.Equal(t, "", periodic.CronSchedule, "configs are not modified")

	// Equal configs loaded again (new pointers) change nothing.
	changes, err = schedule.Apply(feedsOf(
		&feed.Config{ID: "periodic", URL: "a", UpdatePeriod: 6 * time.Hour},
		&feed.Config{ID: "fixed", URL: "b", CronSchedule: "0 3 * * *"},
	))
	require.NoError(t, err)
	assert.True(t, changes.empty())
	assert.Len(t, queue.enqueued, 1)
	assert.Empty(t, c.removed)

	// Changed, removed and added feeds.
	changes, err = schedule.Apply(feedsOf(
		&feed.Config{ID: "periodic", URL: "a", UpdatePeriod: 1 * time.Hour},
		&feed.Config{ID: "added", URL: "c", UpdatePeriod: time.Hour},
	))
	require.NoError(t, err)
	assert.Equal(t, []string{"added"}, changes.Added)
	assert.Equal(t, []string{"periodic"}, changes.Updated)
	assert.Equal(t, []string{"fixed"}, changes.Removed)
	assert.Equal(t, []string{"periodic", "added"}, queue.enqueued, "only the new feed gets an initial update")
	assert.ElementsMatch(t, []string{"@every 1h0m0s", "@every 1h0m0s"}, valuesOf(c.entries))
	assert.Len(t, c.removed, 2, "the changed and the removed feed lose their old entries")
}

func TestFeedScheduleApplyNoBannerEnqueuesExplicitSchedules(t *testing.T) {
	queue := &fakeQueue{}
	schedule := newFeedSchedule(newFakeCron(), queue, true)
	_, err := schedule.Apply(feedsOf(&feed.Config{ID: "fixed", URL: "b", CronSchedule: "0 3 * * *"}))
	require.NoError(t, err)
	assert.Equal(t, []string{"fixed"}, queue.enqueued)
}

func TestFeedScheduleApplyInvalidSchedule(t *testing.T) {
	c := newFakeCron()
	schedule := newFeedSchedule(c, &fakeQueue{}, false)
	changes, err := schedule.Apply(feedsOf(
		&feed.Config{ID: "bad", URL: "a", CronSchedule: "not a schedule"},
		&feed.Config{ID: "good", URL: "b", UpdatePeriod: time.Hour},
	))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `feed "bad"`)
	assert.Equal(t, []string{"good"}, changes.Added, "other feeds are still scheduled")
	assert.Len(t, c.entries, 1)
}

func valuesOf(m map[cron.EntryID]string) []string {
	values := make([]string, 0, len(m))
	for _, v := range m {
		values = append(values, v)
	}
	return values
}

const reloadBaseConfig = `
[server]
port = 8080

[tokens]
youtube = "old-key"

[feeds]
  [feeds.show]
  url = "https://rumble.com/c/show"
`

func newTestReloader(t *testing.T, content string) (*configReloader, *fakeFeedUpdater, *fakeQueue, string) {
	t.Helper()
	path := writeConfigFile(t, "config.toml", content)
	cfg, err := LoadConfig(path)
	require.NoError(t, err)

	queue := &fakeQueue{}
	schedule := newFeedSchedule(newFakeCron(), queue, false)
	_, err = schedule.Apply(cfg.Feeds)
	require.NoError(t, err)
	updater := &fakeFeedUpdater{}
	return &configReloader{path: path, updater: updater, schedule: schedule, current: cfg}, updater, queue, path
}

func TestConfigReloaderAppliesFeedsAndTokens(t *testing.T) {
	reloader, updater, queue, path := newTestReloader(t, reloadBaseConfig)
	var reloaded feedChanges
	reloader.afterReload = func(changes feedChanges) { reloaded = changes }
	assert.Equal(t, 1, reloader.FeedCount())

	require.NoError(t, os.WriteFile(path, []byte(`
[server]
port = 8080

[tokens]
youtube = "new-key"

[feeds]
  [feeds.show]
  url = "https://rumble.com/c/show"
  [feeds.second]
  url = "https://rumble.com/c/second"
`), 0644))
	require.NoError(t, reloader.Reload("test"))

	assert.Equal(t, 2, reloader.FeedCount())
	assert.Len(t, updater.feeds, 2)
	require.Contains(t, updater.keys, model.ProviderYoutube)
	assert.Equal(t, "new-key", updater.keys[model.ProviderYoutube].Get())
	assert.Contains(t, updater.keys, model.ProviderRumble, "Rumble always has a key provider")
	assert.Equal(t, []string{"second"}, reloaded.Added)
	assert.Contains(t, queue.enqueued, "second", "a newly added feed gets an initial update")
}

func TestConfigReloaderKeepsRunningConfigOnError(t *testing.T) {
	reloader, updater, _, path := newTestReloader(t, reloadBaseConfig)
	before := reloader.current

	require.NoError(t, os.WriteFile(path, []byte(reloadBaseConfig+"\n  pagesiz = 3\n"), 0644))
	err := reloader.Reload("test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pagesiz")
	assert.Same(t, before, reloader.current)
	assert.Zero(t, updater.calls, "nothing is applied from an invalid configuration")

	reloader.validate = func(*Config) error { return assert.AnError }
	require.NoError(t, os.WriteFile(path, []byte(reloadBaseConfig), 0644))
	require.Error(t, reloader.Reload("test"), "failed runtime validation also keeps the running configuration")
	assert.Zero(t, updater.calls)
}

func TestConfigReloaderRemovesAllFeeds(t *testing.T) {
	reloader, updater, _, path := newTestReloader(t, reloadBaseConfig)
	require.NoError(t, os.WriteFile(path, []byte("[server]\nport = 8080\n"), 0644))
	require.NoError(t, reloader.Reload("test"))
	assert.Equal(t, 0, reloader.FeedCount())
	assert.Empty(t, updater.feeds)
}

func TestRestartOnlyChanges(t *testing.T) {
	before := &Config{}
	after := &Config{}
	after.Server.Port = 9000
	after.Downloader.Timeout = 5
	after.Feeds = map[string]*feed.Config{"x": {}}
	assert.Equal(t, []string{"server", "downloader"}, restartOnlyChanges(before, after), "feed changes are reloadable")
	assert.Empty(t, restartOnlyChanges(before, &Config{}))
}

func TestWatchConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("a"), 0644))

	changed := make(chan struct{}, 10)
	ctx := t.Context()
	go watchConfigFile(ctx, path, 20*time.Millisecond, func() { changed <- struct{}{} })

	select {
	case <-changed:
		t.Fatal("no change should be reported for an unchanged file")
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, os.WriteFile(path, []byte("bb"), 0644))
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a change notification")
	}

	// Several quick writes settle into a single notification.
	for _, content := range []string{"ccc", "dddd", "eeeee"} {
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a change notification")
	}
	select {
	case <-changed:
		t.Fatal("quick successive writes should produce one notification")
	case <-time.After(150 * time.Millisecond):
	}
}
