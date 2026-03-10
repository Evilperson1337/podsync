package update

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/model"
)

func TestSyncEpisodeMetadataAppliesRetroactiveOverlayWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	storage, err := db.NewBadger(&db.Config{Dir: dir})
	require.NoError(t, err)
	defer storage.Close()

	manager := &Manager{db: storage}
	feed := &model.Feed{
		ID: "feed",
		Episodes: []*model.Episode{{
			ID:          "v1",
			Title:       "Source title",
			Description: "Source description",
			Duration:    123,
			VideoURL:    "https://rumble.com/v1",
			PubDate:     time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			Status:      model.EpisodeDownloaded,
		}},
	}

	require.NoError(t, storage.AddFeed(context.TODO(), feed.ID, feed))
	require.NoError(t, manager.syncEpisodeMetadata(feed.ID, feed.Episodes))

	updated := []*model.Episode{{
		ID:             "v1",
		Title:          "Overlay title",
		Description:    "Overlay description",
		Duration:       999,
		VideoURL:       "https://rumble.com/v1",
		Link:           "https://example.com/item",
		MetadataSource: "rss",
		OrderSource:    "rss_metadata",
		Order:          "1",
		PubDate:        time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC),
	}}

	require.NoError(t, manager.syncEpisodeMetadata(feed.ID, updated))

	episode, err := storage.GetEpisode(context.TODO(), feed.ID, "v1")
	require.NoError(t, err)
	assert.Equal(t, "Overlay title", episode.Title)
	assert.Equal(t, "Overlay description", episode.Description)
	assert.Equal(t, "https://example.com/item", episode.Link)
	assert.EqualValues(t, 123, episode.Duration)
	assert.Equal(t, "rss", episode.MetadataSource)

	count := 0
	require.NoError(t, storage.WalkEpisodes(context.TODO(), feed.ID, func(_ *model.Episode) error {
		count++
		return nil
	}))
	assert.Equal(t, 1, count)
}
