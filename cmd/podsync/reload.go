package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

// newKeyProviders creates API key providers from configured tokens. Rumble needs no key, so it
// always gets an empty provider.
func newKeyProviders(tokens map[model.Provider]StringSlice) (map[model.Provider]feed.KeyProvider, error) {
	keys := map[model.Provider]feed.KeyProvider{}
	for name, list := range tokens {
		provider, err := feed.NewKeyProvider(list)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create key provider for %q", name)
		}
		keys[name] = provider
	}
	if _, ok := keys[model.ProviderRumble]; !ok {
		keys[model.ProviderRumble] = feed.NewStaticKeyProvider("")
	}
	return keys, nil
}

// feedCronSchedule returns the cron spec for a feed: its cron_schedule, or its update_period.
func feedCronSchedule(cfg *feed.Config) string {
	if cfg.CronSchedule != "" {
		return cfg.CronSchedule
	}
	return fmt.Sprintf("@every %s", cfg.UpdatePeriod.String())
}

// feedEnqueuer queues a feed update; implemented by update.Scheduler.
type feedEnqueuer interface {
	Enqueue(cfg *feed.Config) bool
}

// cronScheduler is the subset of cron.Cron used for feed schedules.
type cronScheduler interface {
	AddFunc(spec string, cmd func()) (cron.EntryID, error)
	Remove(id cron.EntryID)
	Entry(id cron.EntryID) cron.Entry
}

type scheduledFeed struct {
	entry  cron.EntryID
	config *feed.Config
}

// feedChanges reports what a schedule update changed, by feed ID.
type feedChanges struct {
	Added   []string
	Updated []string
	Removed []string
}

func (c feedChanges) empty() bool {
	return len(c.Added) == 0 && len(c.Updated) == 0 && len(c.Removed) == 0
}

// feedSchedule keeps cron entries in sync with the configured feeds, at startup and on reload.
type feedSchedule struct {
	mu       sync.Mutex
	cron     cronScheduler
	queue    feedEnqueuer
	noBanner bool
	entries  map[string]scheduledFeed
}

func newFeedSchedule(c cronScheduler, queue feedEnqueuer, noBanner bool) *feedSchedule {
	return &feedSchedule{cron: c, queue: queue, noBanner: noBanner, entries: map[string]scheduledFeed{}}
}

// Apply schedules feeds that are new, reschedules feeds whose configuration changed, and stops
// scheduling feeds that were removed. New feeds get an initial update under the same rule as at
// startup: unless they have an explicit cron_schedule (always in Docker, which runs --no-banner).
// Downloaded episodes of removed feeds are left in storage.
func (s *feedSchedule) Apply(feeds map[string]*feed.Config) (feedChanges, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		changes feedChanges
		result  []string
	)
	for id, scheduled := range s.entries {
		if _, ok := feeds[id]; !ok {
			s.cron.Remove(scheduled.entry)
			delete(s.entries, id)
			changes.Removed = append(changes.Removed, id)
		}
	}

	ids := make([]string, 0, len(feeds))
	for id := range feeds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		cfg := feeds[id]
		existing, exists := s.entries[id]
		if exists && reflect.DeepEqual(existing.config, cfg) {
			continue
		}
		if exists {
			s.cron.Remove(existing.entry)
			delete(s.entries, id)
		}

		schedule := feedCronSchedule(cfg)
		entry, err := s.cron.AddFunc(schedule, func() { s.enqueue(cfg, "scheduled") })
		if err != nil {
			result = append(result, fmt.Sprintf("feed %q: invalid schedule %q: %v", id, schedule, err))
			continue
		}
		s.entries[id] = scheduledFeed{entry: entry, config: cfg}
		log.Debugf("-> %s (update '%s')", id, schedule)

		if exists {
			changes.Updated = append(changes.Updated, id)
			continue
		}
		changes.Added = append(changes.Added, id)
		hasExplicitSchedule := cfg.CronSchedule != ""
		if !hasExplicitSchedule || s.noBanner {
			s.enqueue(cfg, "initial")
		}
	}
	sort.Strings(changes.Removed)
	if len(result) > 0 {
		return changes, errors.New(strings.Join(result, "; "))
	}
	return changes, nil
}

// FeedSchedule describes how a feed is scheduled.
type FeedSchedule struct {
	Spec string
	// NextRun is zero when the feed is not scheduled or cron has not started.
	NextRun time.Time
}

// Lookup returns the schedule of a feed.
func (s *feedSchedule) Lookup(id string) (FeedSchedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scheduled, ok := s.entries[id]
	if !ok {
		return FeedSchedule{}, false
	}
	return FeedSchedule{Spec: feedCronSchedule(scheduled.config), NextRun: s.cron.Entry(scheduled.entry).Next}, true
}

func (s *feedSchedule) enqueue(cfg *feed.Config, reason string) {
	enqueued := s.queue.Enqueue(cfg)
	logger := log.WithFields(log.Fields{"feed_id": cfg.ID, "reason": reason, "enqueued": enqueued})
	if reason == "initial" {
		logger.Info("initial feed update requested")
		return
	}
	if enqueued {
		logger.Debug("feed update requested")
	} else {
		logger.Debug("feed update request deduplicated")
	}
}

// feedUpdater is the part of update.Manager that a reload changes.
type feedUpdater interface {
	SetFeeds(feeds map[string]*feed.Config)
	SetKeys(keys map[model.Provider]feed.KeyProvider)
}

// configReloader re-reads the configuration file and applies the parts that can change while
// running: feeds (including per-feed settings) and API tokens. Other sections need a restart,
// and a warning is logged when they change. An invalid configuration is rejected and the running
// configuration is kept.
type configReloader struct {
	path        string
	updater     feedUpdater
	schedule    *feedSchedule
	validate    func(*Config) error
	afterReload func(changes feedChanges)

	mu      sync.Mutex
	current *Config
	// startup is the configuration Podsync started with. Restart-only sections are compared
	// against it, so a pending restart stays visible across later reloads.
	startup *Config
	// appliedHash identifies the file content currently applied; unchanged content is not
	// reloaded again (for example when the file watcher sees an admin save).
	appliedHash string
}

// configContentHash identifies a version of the configuration file.
func configContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PendingRestart lists sections whose applied changes only take effect after a restart.
func (r *configReloader) PendingRestart() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return restartOnlyChanges(r.startupConfig(), r.current)
}

func (r *configReloader) startupConfig() *Config {
	if r.startup == nil {
		r.startup = r.current
	}
	return r.startup
}

// Current returns the running configuration.
func (r *configReloader) Current() *Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// FeedCount returns the number of feeds in the running configuration.
func (r *configReloader) FeedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.current.Feeds)
}

// Reload loads the configuration file and applies it. It returns the feed changes; content that
// is already applied is skipped.
func (r *configReloader) Reload(reason string) (feedChanges, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	logger := log.WithFields(log.Fields{"config": r.path, "reason": reason})
	fail := func(err error) (feedChanges, error) {
		logger.WithError(err).Error("configuration reload failed; keeping the running configuration")
		return feedChanges{}, err
	}

	data, err := os.ReadFile(r.path)
	if err != nil {
		return fail(err)
	}
	hash := configContentHash(data)
	if r.appliedHash != "" && hash == r.appliedHash {
		logger.Debug("configuration unchanged; nothing to reload")
		return feedChanges{}, nil
	}
	cfg, err := loadConfigData(r.path, data)
	if err != nil {
		return fail(err)
	}
	if r.validate != nil {
		if err := r.validate(cfg); err != nil {
			return fail(err)
		}
	}
	keys, err := newKeyProviders(cfg.Tokens)
	if err != nil {
		return fail(err)
	}

	for _, section := range restartOnlyChanges(r.startupConfig(), cfg) {
		logger.Warnf("changes to [%s] take effect after Podsync is restarted", section)
	}

	r.updater.SetKeys(keys)
	r.updater.SetFeeds(cfg.Feeds)
	changes, scheduleErr := r.schedule.Apply(cfg.Feeds)
	r.current = cfg
	r.appliedHash = hash

	logger.WithFields(log.Fields{
		"feeds":   len(cfg.Feeds),
		"added":   changes.Added,
		"updated": changes.Updated,
		"removed": changes.Removed,
	}).Info("configuration reloaded")
	if len(cfg.Feeds) == 0 {
		logger.Warn("No feeds are configured; add [feeds.<id>] sections to the configuration file.")
	}
	if r.afterReload != nil && !changes.empty() {
		r.afterReload(changes)
	}
	if scheduleErr != nil {
		logger.WithError(scheduleErr).Error("some feeds could not be scheduled")
	}
	return changes, scheduleErr
}

// restartOnlyChanges lists configuration sections that differ between two configurations but are
// only read at startup.
func restartOnlyChanges(before, after *Config) []string {
	sections := []struct {
		name          string
		before, after interface{}
	}{
		{"server", before.Server, after.Server},
		{"storage", before.Storage, after.Storage},
		{"database", before.Database, after.Database},
		{"downloader", before.Downloader, after.Downloader},
		{"log", before.Log, after.Log},
		{"signatures", before.Signatures, after.Signatures},
		{"audiobookshelf", before.Audiobookshelf, after.Audiobookshelf},
		{"admin", before.Admin, after.Admin},
	}
	var changed []string
	for _, section := range sections {
		if !reflect.DeepEqual(section.before, section.after) {
			changed = append(changed, section.name)
		}
	}
	return changed
}

// configFileState identifies a version of the configuration file on disk.
type configFileState struct {
	exists  bool
	size    int64
	modTime time.Time
}

func statConfigFile(path string) configFileState {
	info, err := os.Stat(path)
	if err != nil {
		return configFileState{}
	}
	return configFileState{exists: true, size: info.Size(), modTime: info.ModTime()}
}

// watchConfigFile polls path and calls onChange after the file changes. A change is only acted
// on once the file has stopped changing for one interval, so a partially saved file is not read.
func watchConfigFile(ctx context.Context, path string, interval time.Duration, onChange func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	applied := statConfigFile(path)
	pending := applied
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			current := statConfigFile(path)
			if current == applied {
				pending = current
				continue
			}
			if current != pending {
				// Still changing; wait for it to settle.
				pending = current
				continue
			}
			applied = current
			if current.exists {
				onChange()
			}
		}
	}
}
