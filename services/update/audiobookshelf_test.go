package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/audiobookshelf"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
)

type absFixture struct {
	manager    *Manager
	downloader *fakeDownloader
	storage    *fs.Local
	absRoot    string
	feedConfig *feed.Config
}

func newABSFixture(t *testing.T, feedEnabled bool, globalEnabled bool) *absFixture {
	t.Helper()
	storage := newTestLocalStorage(t)
	absRoot := t.TempDir()
	feedConfig := &feed.Config{
		ID:             "doctrine",
		URL:            "https://youtube.com/channel/doctrine",
		Format:         model.FormatAudio,
		PageSize:       10,
		Audiobookshelf: audiobookshelf.FeedConfig{Enabled: feedEnabled, Directory: "Doctrine"},
	}
	downloader := &fakeDownloader{content: map[string]string{"ep-1": "audio-one"}}
	manager := &Manager{
		hostname:   "https://podsync.test",
		downloader: downloader,
		db:         newTestDB(t),
		fs:         storage,
		feeds:      map[string]*feed.Config{feedConfig.ID: feedConfig},
		buildFeed: func(_ context.Context, _ *feed.Config) (*model.Feed, error) {
			return &model.Feed{
				ID:     feedConfig.ID,
				Title:  "Doctrine",
				Format: model.FormatAudio,
				Episodes: []*model.Episode{{
					ID:       "ep-1",
					Title:    "Episode One",
					VideoURL: "https://example.com/video/ep-1",
					PubDate:  time.Date(2026, 1, 2, 14, 0, 0, 0, time.UTC),
					Status:   model.EpisodeNew,
				}},
			}, nil
		},
	}
	if globalEnabled {
		manager.SetAudiobookshelfExporter(audiobookshelf.NewExporter(absRoot))
	}
	return &absFixture{manager: manager, downloader: downloader, storage: storage, absRoot: absRoot, feedConfig: feedConfig}
}

func (f *absFixture) sourcePath() string {
	return filepath.Join(f.storage.RootDir(), "doctrine", "ep-1.mp3")
}

func (f *absFixture) destinationPath() string {
	return filepath.Join(f.absRoot, "Doctrine", "ep-1.mp3")
}

func (f *absFixture) requireEpisodeStatus(t *testing.T, status model.EpisodeStatus) {
	t.Helper()
	episode, err := f.manager.db.GetEpisode(context.Background(), f.feedConfig.ID, "ep-1")
	require.NoError(t, err)
	assert.Equal(t, status, episode.Status)
}

func requireHardlinked(t *testing.T, a, b string) {
	t.Helper()
	aInfo, err := os.Stat(a)
	require.NoError(t, err)
	bInfo, err := os.Stat(b)
	require.NoError(t, err)
	assert.True(t, os.SameFile(aInfo, bInfo), "%s and %s should share an inode", a, b)
}

func requireEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestAudiobookshelfExportAfterDownload(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, true)

	if runtime.GOOS != "windows" {
		// The hook proves the hardlink already exists when post-download hooks run.
		marker := filepath.Join(t.TempDir(), "hook-saw-link")
		fx.feedConfig.PostEpisodeDownload = []*feed.ExecHook{{
			Command: []string{"sh", "-c", `test -f "$1" && touch "$2"`, "sh", fx.destinationPath(), marker},
		}}
		t.Cleanup(func() {
			_, err := os.Stat(marker)
			assert.NoError(t, err, "hardlink should exist before post-download hooks run")
		})
	}

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodePublished)
	requireHardlinked(t, fx.sourcePath(), fx.destinationPath())
	assertFileContents(t, fx.destinationPath(), "audio-one")
}

func TestAudiobookshelfExportFailureDoesNotFailEpisode(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, true)
	require.NoError(t, os.MkdirAll(filepath.Dir(fx.destinationPath()), 0755))
	require.NoError(t, os.WriteFile(fx.destinationPath(), []byte("unrelated"), 0644))

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodePublished)
	assertFileContents(t, fx.sourcePath(), "audio-one")
	assertFileContents(t, fx.destinationPath(), "unrelated")

	// A later run must not re-download valid media because of the export conflict.
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	assert.Len(t, fx.downloader.called, 1)
	fx.requireEpisodeStatus(t, model.EpisodePublished)
	assertFileContents(t, fx.destinationPath(), "unrelated")
}

func TestAudiobookshelfBackfillsPreviouslyStoredEpisode(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, false)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	fx.requireEpisodeStatus(t, model.EpisodePublished)
	requireEmptyDir(t, fx.absRoot)

	fx.manager.SetAudiobookshelfExporter(audiobookshelf.NewExporter(fx.absRoot))
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	assert.Len(t, fx.downloader.called, 1, "backfill must not re-download media")
	fx.requireEpisodeStatus(t, model.EpisodePublished)
	requireHardlinked(t, fx.sourcePath(), fx.destinationPath())
}

func skipWithoutInodes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("device/inode records are not available on Windows")
	}
}

func TestAudiobookshelfRecordsLink(t *testing.T) {
	skipWithoutInodes(t)
	t.Parallel()
	fx := newABSFixture(t, true, true)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	episode, err := fx.manager.db.GetEpisode(context.Background(), fx.feedConfig.ID, "ep-1")
	require.NoError(t, err)
	require.NotNil(t, episode.AudiobookshelfLink)
	assert.Equal(t, fx.destinationPath(), episode.AudiobookshelfLink.Path)
	assert.NotZero(t, episode.AudiobookshelfLink.Inode)
}

func TestAudiobookshelfDeletionInLibraryRemovesPodsyncCopy(t *testing.T) {
	skipWithoutInodes(t)
	t.Parallel()
	fx := newABSFixture(t, true, true)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	require.NoError(t, os.Remove(fx.destinationPath()))

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodeCleaned)
	_, err := os.Stat(fx.sourcePath())
	assert.True(t, os.IsNotExist(err), "Podsync copy should mirror the Audiobookshelf deletion")
	_, err = os.Stat(fx.destinationPath())
	assert.True(t, os.IsNotExist(err), "the deleted link must not be recreated")

	xmlBytes, err := os.ReadFile(filepath.Join(fx.storage.RootDir(), "doctrine.xml"))
	require.NoError(t, err)
	assert.NotContains(t, string(xmlBytes), "ep-1.mp3")

	// The episode is not downloaded again on later runs.
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	assert.Len(t, fx.downloader.called, 1)
	_, err = os.Stat(fx.destinationPath())
	assert.True(t, os.IsNotExist(err))
}

func TestAudiobookshelfManualPodsyncDeletionRemovesLink(t *testing.T) {
	skipWithoutInodes(t)
	t.Parallel()
	fx := newABSFixture(t, true, true)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	require.NoError(t, os.Remove(fx.sourcePath()))

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodeCleaned)
	_, err := os.Stat(fx.destinationPath())
	assert.True(t, os.IsNotExist(err), "Audiobookshelf copy should mirror the Podsync deletion")
	assert.Len(t, fx.downloader.called, 1)
}

func TestAudiobookshelfManualPodsyncDeletionKeepsUnrecordedFile(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, false)

	// Podsync has an episode; the library holds a same-named file Podsync never recorded.
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	require.NoError(t, os.MkdirAll(filepath.Dir(fx.destinationPath()), 0755))
	require.NoError(t, os.Link(fx.sourcePath(), fx.destinationPath()))
	require.NoError(t, os.Remove(fx.sourcePath()))

	fx.manager.SetAudiobookshelfExporter(audiobookshelf.NewExporter(fx.absRoot))
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodeCleaned)
	assertFileContents(t, fx.destinationPath(), "audio-one")
}

func TestAudiobookshelfUnmountedLibraryDeletesNothing(t *testing.T) {
	skipWithoutInodes(t)
	t.Parallel()
	fx := newABSFixture(t, true, true)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	// Point the exporter at a library root that is not mounted.
	fx.manager.SetAudiobookshelfExporter(audiobookshelf.NewExporter(filepath.Join(fx.absRoot, "unmounted")))

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodePublished)
	assertFileContents(t, fx.sourcePath(), "audio-one")
	requireHardlinked(t, fx.sourcePath(), fx.destinationPath())
}

func TestAudiobookshelfExistingLinkIsNoop(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, true)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	before, err := os.Stat(fx.destinationPath())
	require.NoError(t, err)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	after, err := os.Stat(fx.destinationPath())
	require.NoError(t, err)

	assert.True(t, os.SameFile(before, after))
	assert.Len(t, fx.downloader.called, 1)
	entries, err := os.ReadDir(filepath.Dir(fx.destinationPath()))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no duplicate files should be created")
}

func TestAudiobookshelfReconcileConflictIsNonFatal(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, false)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	require.NoError(t, os.MkdirAll(filepath.Dir(fx.destinationPath()), 0755))
	require.NoError(t, os.WriteFile(fx.destinationPath(), []byte("unrelated"), 0644))

	fx.manager.SetAudiobookshelfExporter(audiobookshelf.NewExporter(fx.absRoot))
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodePublished)
	assertFileContents(t, fx.destinationPath(), "unrelated")
}

func TestAudiobookshelfExportOnReusedMedia(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, true)
	require.NoError(t, os.MkdirAll(filepath.Dir(fx.sourcePath()), 0755))
	require.NoError(t, os.WriteFile(fx.sourcePath(), []byte("existing"), 0644))

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	assert.Empty(t, fx.downloader.called)
	fx.requireEpisodeStatus(t, model.EpisodePublished)
	requireHardlinked(t, fx.sourcePath(), fx.destinationPath())
}

func TestAudiobookshelfFeedDisabledIsUnchanged(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, false, true)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodePublished)
	requireEmptyDir(t, fx.absRoot)
}

func TestAudiobookshelfGlobalDisabledIsUnchanged(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, false)

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodePublished)
	requireEmptyDir(t, fx.absRoot)
}

// withTwoEpisodes makes the feed return an older ep-1 and newer ep-2, with keep_last = 1,
// so every run cleans ep-1.
func (f *absFixture) withTwoEpisodes() {
	f.downloader.content["ep-2"] = "audio-two"
	f.feedConfig.Clean = &feed.Cleanup{KeepLast: 1}
	f.manager.buildFeed = func(_ context.Context, _ *feed.Config) (*model.Feed, error) {
		return &model.Feed{
			ID:     f.feedConfig.ID,
			Title:  "Doctrine",
			Format: model.FormatAudio,
			Episodes: []*model.Episode{
				{ID: "ep-1", Title: "Episode One", VideoURL: "https://example.com/video/ep-1", PubDate: time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC), Status: model.EpisodeNew},
				{ID: "ep-2", Title: "Episode Two", VideoURL: "https://example.com/video/ep-2", PubDate: time.Date(2026, 1, 2, 14, 0, 0, 0, time.UTC), Status: model.EpisodeNew},
			},
		}, nil
	}
}

func TestAudiobookshelfCleanupRemovesLink(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, true)
	fx.withTwoEpisodes()

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodeCleaned)
	_, err := os.Stat(fx.sourcePath())
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(fx.destinationPath())
	assert.True(t, os.IsNotExist(err), "cleaned episode must be removed from Audiobookshelf too")

	requireHardlinked(t,
		filepath.Join(fx.storage.RootDir(), "doctrine", "ep-2.mp3"),
		filepath.Join(fx.absRoot, "Doctrine", "ep-2.mp3"))
}

func TestAudiobookshelfCleanupLeavesUnrelatedFile(t *testing.T) {
	t.Parallel()
	fx := newABSFixture(t, true, true)
	fx.withTwoEpisodes()
	require.NoError(t, os.MkdirAll(filepath.Dir(fx.destinationPath()), 0755))
	require.NoError(t, os.WriteFile(fx.destinationPath(), []byte("unrelated"), 0644))

	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodeCleaned)
	_, err := os.Stat(fx.sourcePath())
	assert.True(t, os.IsNotExist(err))
	assertFileContents(t, fx.destinationPath(), "unrelated")
}

func TestAudiobookshelfCleanupRetriesWhenRemovalFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX permissions enforced for a non-root user")
	}
	t.Parallel()
	fx := newABSFixture(t, true, true)
	fx.withTwoEpisodes()
	fx.feedConfig.Clean = nil

	// First run links both episodes without cleaning anything.
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))
	requireHardlinked(t, fx.sourcePath(), fx.destinationPath())

	absDir := filepath.Dir(fx.destinationPath())
	require.NoError(t, os.Chmod(absDir, 0555))
	t.Cleanup(func() { _ = os.Chmod(absDir, 0755) })

	fx.feedConfig.Clean = &feed.Cleanup{KeepLast: 1}
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	// Removal failed, so Podsync keeps its copy and both stay mirrored.
	fx.requireEpisodeStatus(t, model.EpisodePublished)
	requireHardlinked(t, fx.sourcePath(), fx.destinationPath())

	require.NoError(t, os.Chmod(absDir, 0755))
	require.NoError(t, fx.manager.Update(context.Background(), fx.feedConfig))

	fx.requireEpisodeStatus(t, model.EpisodeCleaned)
	_, err := os.Stat(fx.destinationPath())
	assert.True(t, os.IsNotExist(err))
}
