package update

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/ytdl"
)

type fakeDownloader struct {
	mu       sync.Mutex
	content  map[string]string
	failures map[string]error
	called   []string
}

func (d *fakeDownloader) Download(_ context.Context, _ *feed.Config, episode *model.Episode) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.called = append(d.called, episode.ID)
	if err := d.failures[episode.ID]; err != nil {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(d.content[episode.ID])), nil
}

func (d *fakeDownloader) PlaylistMetadata(_ context.Context, _ string) (ytdl.PlaylistMetadata, error) {
	return ytdl.PlaylistMetadata{}, nil
}

func TestManagerUpdateSuccessfulWorkflow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database := newTestDB(t)
	storage := newTestLocalStorage(t)
	feedConfig := &feed.Config{
		ID:       "sample",
		URL:      "https://youtube.com/channel/sample",
		Format:   model.FormatAudio,
		PageSize: 10,
		OPML:     true,
		Custom:   feed.Custom{Author: "Podcast Author"},
	}

	manager := &Manager{
		hostname: "https://podsync.test",
		downloader: &fakeDownloader{content: map[string]string{
			"ep-1": "audio-one",
			"ep-2": "audio-two",
		}},
		db:    database,
		fs:    storage,
		feeds: map[string]*feed.Config{feedConfig.ID: feedConfig},
		buildFeed: func(_ context.Context, _ *feed.Config) (*model.Feed, error) {
			return &model.Feed{
				ID:          feedConfig.ID,
				Title:       "Sample Feed",
				Description: "Feed description",
				Author:      "Feed author",
				ItemURL:     "https://example.com/channel",
				PubDate:     time.Date(2026, 1, 2, 15, 0, 0, 0, time.UTC),
				Format:      model.FormatAudio,
				Episodes: []*model.Episode{
					{
						ID:             "ep-1",
						Title:          "Episode One",
						Description:    "Description One",
						Subtitle:       "Subtitle One",
						MetadataSource: "rss",
						Season:         2,
						EpisodeNumber:  7,
						EpisodeType:    "full",
						VideoURL:       "https://example.com/video/ep-1",
						PubDate:        time.Date(2026, 1, 2, 14, 0, 0, 0, time.UTC),
						Status:         model.EpisodeNew,
					},
					{
						ID:          "ep-2",
						Title:       "Episode Two",
						Description: "Description Two",
						VideoURL:    "https://example.com/video/ep-2",
						PubDate:     time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC),
						Status:      model.EpisodeNew,
					},
				},
			}, nil
		},
	}

	require.NoError(t, manager.Update(ctx, feedConfig))

	episode, err := database.GetEpisode(ctx, feedConfig.ID, "ep-1")
	require.NoError(t, err)
	assert.Equal(t, model.EpisodePublished, episode.Status)
	assert.EqualValues(t, len("audio-one"), episode.Size)

	xmlBytes, err := os.ReadFile(filepath.Join(storage.RootDir(), "sample.xml"))
	require.NoError(t, err)
	xmlText := string(xmlBytes)
	assert.Contains(t, xmlText, `<itunes:season>2</itunes:season>`)
	assert.Contains(t, xmlText, `<itunes:episode>7</itunes:episode>`)
	assert.Contains(t, xmlText, `<itunes:episodeType>full</itunes:episodeType>`)
	assert.Contains(t, xmlText, `https://podsync.test/sample/ep-1.mp3`)

	opmlBytes, err := os.ReadFile(filepath.Join(storage.RootDir(), "podsync.opml"))
	require.NoError(t, err)
	assert.Contains(t, string(opmlBytes), `https://podsync.test/sample.xml`)

	mediaBytes, err := os.ReadFile(filepath.Join(storage.RootDir(), "sample", "ep-1.mp3"))
	require.NoError(t, err)
	assert.Equal(t, "audio-one", string(mediaBytes))
}

func TestManagerUpdateDownloadFailureIsRetrySafe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database := newTestDB(t)
	storage := newTestLocalStorage(t)
	downloader := &fakeDownloader{
		content:  map[string]string{"ep-1": "audio-one"},
		failures: map[string]error{"ep-1": assert.AnError},
	}
	feedConfig := &feed.Config{ID: "sample", URL: "https://youtube.com/channel/sample", Format: model.FormatAudio, PageSize: 10}

	manager := &Manager{
		hostname:   "https://podsync.test",
		downloader: downloader,
		db:         database,
		fs:         storage,
		feeds:      map[string]*feed.Config{feedConfig.ID: feedConfig},
		buildFeed: func(_ context.Context, _ *feed.Config) (*model.Feed, error) {
			return &model.Feed{
				ID:      feedConfig.ID,
				Title:   "Sample Feed",
				ItemURL: "https://example.com/channel",
				Episodes: []*model.Episode{{
					ID:          "ep-1",
					Title:       "Episode One",
					Description: "Description One",
					VideoURL:    "https://example.com/video/ep-1",
					PubDate:     time.Date(2026, 1, 2, 14, 0, 0, 0, time.UTC),
					Status:      model.EpisodeNew,
				}},
			}, nil
		},
	}

	require.NoError(t, manager.Update(ctx, feedConfig))

	episode, err := database.GetEpisode(ctx, feedConfig.ID, "ep-1")
	require.NoError(t, err)
	assert.Equal(t, model.EpisodeError, episode.Status)
	storedFeed, err := database.GetFeed(ctx, feedConfig.ID)
	require.NoError(t, err)
	assert.False(t, storedFeed.LastFailureAt.IsZero())
	assert.NotEmpty(t, storedFeed.LastFailure)
	_, err = os.Stat(filepath.Join(storage.RootDir(), "sample", "ep-1.mp3"))
	assert.True(t, os.IsNotExist(err))

	downloader.failures = map[string]error{}
	require.NoError(t, manager.Update(ctx, feedConfig))

	episode, err = database.GetEpisode(ctx, feedConfig.ID, "ep-1")
	require.NoError(t, err)
	assert.Equal(t, model.EpisodePublished, episode.Status)
	storedFeed, err = database.GetFeed(ctx, feedConfig.ID)
	require.NoError(t, err)
	assert.False(t, storedFeed.LastSuccessAt.IsZero())
	assert.Empty(t, storedFeed.LastFailure)
	mediaBytes, err := os.ReadFile(filepath.Join(storage.RootDir(), "sample", "ep-1.mp3"))
	require.NoError(t, err)
	assert.Equal(t, "audio-one", string(mediaBytes))
	assert.Equal(t, []string{"ep-1", "ep-1"}, downloader.called)
}

func TestManagerUpdateCleanupRemovesOldFilesAndKeepsDatabaseState(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database := newTestDB(t)
	storage := newTestLocalStorage(t)
	downloader := &fakeDownloader{content: map[string]string{
		"ep-1": "audio-one",
		"ep-2": "audio-two",
		"ep-3": "audio-three",
	}}
	feedConfig := &feed.Config{
		ID:       "sample",
		URL:      "https://youtube.com/channel/sample",
		Format:   model.FormatAudio,
		PageSize: 10,
		Clean:    &feed.Cleanup{KeepLast: 2},
	}

	var run int
	manager := &Manager{
		hostname:   "https://podsync.test",
		downloader: downloader,
		db:         database,
		fs:         storage,
		feeds:      map[string]*feed.Config{feedConfig.ID: feedConfig},
		buildFeed: func(_ context.Context, _ *feed.Config) (*model.Feed, error) {
			run++
			episodes := []*model.Episode{
				{ID: "ep-1", Title: "Episode One", Description: "One", VideoURL: "https://example.com/1", PubDate: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), Status: model.EpisodeNew},
				{ID: "ep-2", Title: "Episode Two", Description: "Two", VideoURL: "https://example.com/2", PubDate: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC), Status: model.EpisodeNew},
			}
			if run > 1 {
				episodes = append(episodes, &model.Episode{ID: "ep-3", Title: "Episode Three", Description: "Three", VideoURL: "https://example.com/3", PubDate: time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC), Status: model.EpisodeNew})
			}
			return &model.Feed{ID: feedConfig.ID, Title: "Sample Feed", ItemURL: "https://example.com/channel", Episodes: episodes}, nil
		},
	}

	require.NoError(t, manager.Update(ctx, feedConfig))
	require.NoError(t, manager.Update(ctx, feedConfig))

	_, err := os.Stat(filepath.Join(storage.RootDir(), "sample", "ep-1.mp3"))
	assert.True(t, os.IsNotExist(err))

	episode, err := database.GetEpisode(ctx, feedConfig.ID, "ep-1")
	require.NoError(t, err)
	assert.Equal(t, model.EpisodeCleaned, episode.Status)
	assert.Empty(t, episode.Title)
	assert.Empty(t, episode.Description)

	assertFileContents(t, filepath.Join(storage.RootDir(), "sample", "ep-2.mp3"), "audio-two")
	assertFileContents(t, filepath.Join(storage.RootDir(), "sample", "ep-3.mp3"), "audio-three")
}

func newTestDB(t *testing.T) db.Storage {
	t.Helper()
	database, err := db.NewBadger(&db.Config{Dir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})
	return database
}

func newTestLocalStorage(t *testing.T) *fs.Local {
	t.Helper()
	storage, err := fs.NewLocal(t.TempDir(), false)
	require.NoError(t, err)
	return storage
}

func assertFileContents(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, expected, string(bytes.TrimSpace(data)))
}
