package admin

import (
	"context"
	"errors"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

// FeedRuntime is a configured feed with its current schedule.
type FeedRuntime struct {
	Config   *feed.Config
	Schedule string
	// NextRun is zero when the feed is not scheduled.
	NextRun time.Time
}

// Runtime exposes the running configuration to the admin interface. It reflects reloads.
type Runtime interface {
	Feeds() []FeedRuntime
}

// Status is the dashboard payload.
type Status struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Version     string       `json:"version"`
	ConfigPath  string       `json:"config_path"`
	Feeds       []FeedStatus `json:"feeds"`
}

// FeedStatus summarizes one feed for the dashboard.
type FeedStatus struct {
	ID             string                `json:"id"`
	URL            string                `json:"url"`
	Title          string                `json:"title,omitempty"`
	Format         string                `json:"format"`
	Schedule       string                `json:"schedule"`
	NextRun        *time.Time            `json:"next_run,omitempty"`
	LastSuccessAt  *time.Time            `json:"last_success_at,omitempty"`
	LastFailureAt  *time.Time            `json:"last_failure_at,omitempty"`
	LastFailure    string                `json:"last_failure,omitempty"`
	Synced         bool                  `json:"synced"`
	Episodes       map[string]int        `json:"episodes"`
	Audiobookshelf *AudiobookshelfStatus `json:"audiobookshelf,omitempty"`
}

// AudiobookshelfStatus summarizes a feed's Audiobookshelf export.
type AudiobookshelfStatus struct {
	Directory string `json:"directory"`
	// Linked counts episodes with a recorded Audiobookshelf hardlink.
	Linked int `json:"linked"`
}

func buildStatus(ctx context.Context, database db.Storage, runtime Runtime) Status {
	feeds := runtime.Feeds()
	sort.Slice(feeds, func(i, j int) bool { return feeds[i].Config.ID < feeds[j].Config.ID })

	status := Status{GeneratedAt: time.Now().UTC(), Feeds: make([]FeedStatus, 0, len(feeds))}
	for _, runtimeFeed := range feeds {
		status.Feeds = append(status.Feeds, buildFeedStatus(ctx, database, runtimeFeed))
	}
	return status
}

func buildFeedStatus(ctx context.Context, database db.Storage, runtimeFeed FeedRuntime) FeedStatus {
	cfg := runtimeFeed.Config
	status := FeedStatus{
		ID:       cfg.ID,
		URL:      cfg.URL,
		Format:   string(cfg.Format),
		Schedule: runtimeFeed.Schedule,
		NextRun:  timePtr(runtimeFeed.NextRun),
		Episodes: map[string]int{},
	}
	if cfg.Audiobookshelf.Enabled {
		status.Audiobookshelf = &AudiobookshelfStatus{Directory: cfg.Audiobookshelf.Directory}
	}

	stored, err := database.GetFeed(ctx, cfg.ID)
	if err != nil {
		if !errors.Is(err, model.ErrNotFound) {
			log.WithError(err).WithField("feed_id", cfg.ID).Warn("admin status: failed to read feed")
		}
		return status
	}
	status.Synced = true
	status.Title = stored.Title
	status.LastSuccessAt = timePtr(stored.LastSuccessAt)
	status.LastFailureAt = timePtr(stored.LastFailureAt)
	status.LastFailure = stored.LastFailure
	for _, episode := range stored.Episodes {
		status.Episodes[string(episode.Status)]++
		if status.Audiobookshelf != nil && episode.AudiobookshelfLink != nil {
			status.Audiobookshelf.Linked++
		}
	}
	return status
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
