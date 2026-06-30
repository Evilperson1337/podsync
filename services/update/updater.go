package update

import (
	"context"
	"expvar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/builder"
	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/overlay"
	"github.com/mxpv/podsync/pkg/ytdl"
)

var (
	metricPublicationXMLBuilds  = expvar.NewInt("publication_xml_builds_total")
	metricPublicationOPMLBuilds = expvar.NewInt("publication_opml_builds_total")
	metricReconciledEpisodes    = expvar.NewInt("reconciled_episodes_total")
	metricFeedRunSuccesses      = expvar.NewInt("feed_run_successes_total")
	metricFeedRunFailures       = expvar.NewInt("feed_run_failures_total")
)

type Downloader interface {
	Download(ctx context.Context, feedConfig *feed.Config, episode *model.Episode) (io.ReadCloser, error)
	PlaylistMetadata(ctx context.Context, url string) (metadata ytdl.PlaylistMetadata, err error)
}

type TokenList []string

type Manager struct {
	hostname    string
	downloader  Downloader
	db          db.Storage
	fs          fs.Storage
	feeds       map[string]*feed.Config
	keys        map[model.Provider]feed.KeyProvider
	sigDir      string
	overlay     *overlay.Manager
	buildFeed   func(ctx context.Context, cfg *feed.Config) (*model.Feed, error)
	opml        *OPMLPublisher
	publication *PublicationService
}

type FeedSyncSummary struct {
	FeedID              string
	FeedTitle           string
	SourceItemsFound    int
	NewItemsDiscovered  int
	AlreadyKnown        int
	Downloaded          int
	ReusedExistingMedia int
	Skipped             int
	Excluded            int
	Failed              int
	GeneratedFeedItems  int
	Duration            time.Duration
}

type feedDiscoverySummary struct {
	SourceItemsFound   int
	NewItemsDiscovered int
	AlreadyKnown       int
	FeedTitle          string
}

type downloadSummary struct {
	Downloaded          int
	ReusedExistingMedia int
	Skipped             int
	Excluded            int
	Failed              int
}

func NewUpdater(
	feeds map[string]*feed.Config,
	keys map[model.Provider]feed.KeyProvider,
	hostname string,
	signaturesRoot string,
	downloader Downloader,
	db db.Storage,
	storage fs.Storage,
) (*Manager, error) {
	sigDir := strings.TrimSpace(signaturesRoot)
	if sigDir == "" {
		sigDir = strings.TrimSpace(os.Getenv("PODSYNC_SIGNATURES_DIR"))
	}
	if sigDir == "" {
		if localFS, ok := storage.(*fs.Local); ok {
			sigDir = localFS.RootDir()
		}
	}
	if localFS, ok := storage.(*fs.Local); ok && sigDir == "" {
		sigDir = localFS.RootDir()
	}
	if sigDir != "" {
		log.WithFields(log.Fields{
			"signatures_root": sigDir,
			"signatures_dir":  filepath.Join(sigDir, "<feed_id>", "signatures"),
		}).Info("signature trim enabled")
	}
	return &Manager{
		hostname:    hostname,
		downloader:  downloader,
		db:          db,
		fs:          storage,
		feeds:       feeds,
		keys:        keys,
		sigDir:      sigDir,
		overlay:     overlay.NewDefaultManager(nil),
		publication: NewPublicationService(db, storage, feeds, hostname),
		buildFeed: func(ctx context.Context, cfg *feed.Config) (*model.Feed, error) {
			return defaultBuildFeed(ctx, cfg, keys, downloader)
		},
	}, nil
}

func defaultBuildFeed(ctx context.Context, feedConfig *feed.Config, keys map[model.Provider]feed.KeyProvider, downloader Downloader) (*model.Feed, error) {
	info, err := builder.ParseURL(feedConfig.URL)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse URL: %s", feedConfig.URL)
	}

	keyProvider, ok := keys[info.Provider]
	if !ok {
		return nil, errors.Errorf("key provider %q not loaded", info.Provider)
	}

	provider, err := builder.New(ctx, info.Provider, keyProvider.Get(), downloader)
	if err != nil {
		return nil, err
	}

	return provider.Build(ctx, feedConfig)
}

func (u *Manager) SetOPMLPublisher(publisher *OPMLPublisher) {
	u.opml = publisher
}

func (u *Manager) Update(ctx context.Context, feedConfig *feed.Config) error {
	_, err := u.UpdateWithSummary(ctx, feedConfig)
	return err
}

func (u *Manager) UpdateWithSummary(ctx context.Context, feedConfig *feed.Config) (*FeedSyncSummary, error) {
	logger := loggerWithExecution(ctx, log.Fields{
		"feed_id": feedConfig.ID,
		"format":  feedConfig.Format,
		"quality": feedConfig.Quality,
	})
	logger.Infof("-> updating %s", feedConfig.URL)

	started := time.Now()
	summary := &FeedSyncSummary{FeedID: feedConfig.ID}
	if err := u.reconcileFeedState(ctx, feedConfig); err != nil {
		_ = u.recordFeedRunFailure(ctx, feedConfig.ID, err)
		return summary, errors.Wrap(err, "reconcile failed")
	}

	discovery, err := u.updateFeed(ctx, feedConfig)
	if err != nil {
		_ = u.recordFeedRunFailure(ctx, feedConfig.ID, err)
		return summary, errors.Wrap(err, "update failed")
	}
	summary.SourceItemsFound = discovery.SourceItemsFound
	summary.NewItemsDiscovered = discovery.NewItemsDiscovered
	summary.AlreadyKnown = discovery.AlreadyKnown
	summary.FeedTitle = discovery.FeedTitle

	// Fetch episodes for download
	episodesToDownload, err := u.fetchEpisodes(ctx, feedConfig)
	if err != nil {
		return summary, errors.Wrap(err, "fetch episodes failed")
	}

	downloads, err := u.downloadEpisodes(ctx, feedConfig, episodesToDownload)
	if err != nil {
		_ = u.recordFeedRunFailure(ctx, feedConfig.ID, err)
		return summary, errors.Wrap(err, "download failed")
	}
	summary.Downloaded = downloads.Downloaded
	summary.ReusedExistingMedia = downloads.ReusedExistingMedia
	summary.Skipped = downloads.Skipped
	summary.Excluded = downloads.Excluded
	summary.Failed = downloads.Failed

	if err := u.cleanup(ctx, feedConfig); err != nil {
		log.WithError(err).Error("cleanup failed")
	}

	if err := u.buildXML(ctx, feedConfig); err != nil {
		_ = u.recordFeedRunFailure(ctx, feedConfig.ID, err)
		return summary, errors.Wrap(err, "xml build failed")
	}
	summary.GeneratedFeedItems = u.countPublishableEpisodes(ctx, feedConfig.ID)

	if u.shouldBuildOPML(feedConfig) {
		if u.opml != nil {
			u.opml.Request(ctx)
		} else {
			if err := u.buildOPML(ctx); err != nil {
				_ = u.recordFeedRunFailure(ctx, feedConfig.ID, err)
				return summary, errors.Wrap(err, "opml build failed")
			}
		}
	}

	elapsed := time.Since(started)
	summary.Duration = elapsed
	_ = u.recordFeedRunSuccess(ctx, feedConfig.ID)
	logFeedSyncSummary(logger, summary)
	return summary, nil
}

func (u *Manager) recordFeedRunSuccess(ctx context.Context, feedID string) error {
	feedModel, err := u.db.GetFeed(ctx, feedID)
	if err != nil {
		return err
	}
	feedModel.LastSuccessAt = time.Now().UTC()
	hasErroredEpisodes, err := u.feedHasErroredEpisodes(ctx, feedID)
	if err != nil {
		return err
	}
	metricFeedRunSuccesses.Add(1)
	if !hasErroredEpisodes {
		feedModel.LastFailureAt = time.Time{}
		feedModel.LastFailure = ""
	}
	return u.db.AddFeed(ctx, feedID, feedModel)
}

func (u *Manager) recordFeedRunFailure(ctx context.Context, feedID string, runErr error) error {
	feedModel, err := u.db.GetFeed(ctx, feedID)
	if err != nil {
		return err
	}
	feedModel.LastFailureAt = time.Now().UTC()
	if runErr != nil {
		feedModel.LastFailure = runErr.Error()
	}
	metricFeedRunFailures.Add(1)
	return u.db.AddFeed(ctx, feedID, feedModel)
}

func (u *Manager) feedHasErroredEpisodes(ctx context.Context, feedID string) (bool, error) {
	hasFailure := false
	err := u.db.WalkEpisodes(ctx, feedID, func(episode *model.Episode) error {
		if episode.Status == model.EpisodeError {
			hasFailure = true
		}
		return nil
	})
	return hasFailure, err
}

func (u *Manager) shouldBuildOPML(feedConfig *feed.Config) bool {
	if feedConfig != nil && feedConfig.OPML {
		return true
	}
	for _, cfg := range u.feeds {
		if cfg != nil && cfg.OPML {
			return false
		}
	}
	return true
}

// updateFeed pulls API for new episodes and saves them to database
func (u *Manager) updateFeed(ctx context.Context, feedConfig *feed.Config) (feedDiscoverySummary, error) {
	logger := loggerWithExecution(ctx, log.Fields{"feed_id": feedConfig.ID})
	logger.Debug("building feed")
	result, err := u.buildFeed(ctx, feedConfig)
	if err != nil {
		return feedDiscoverySummary{}, err
	}
	discovery := feedDiscoverySummary{SourceItemsFound: len(result.Episodes), FeedTitle: result.Title}

	logger.WithFields(log.Fields{"episodes": len(result.Episodes), "title": result.Title}).Debug("received episodes from builder")

	if err := u.overlay.Apply(ctx, feedConfig, result); err != nil {
		return discovery, err
	}

	episodeSet := make(map[string]struct{})
	knownEpisodes := make(map[string]*model.Episode)
	if err := u.db.WalkEpisodes(ctx, feedConfig.ID, func(episode *model.Episode) error {
		knownEpisodes[episode.ID] = episode
		if !model.IsEpisodePublishable(episode.Status) && episode.Status != model.EpisodeCleaned {
			episodeSet[episode.ID] = struct{}{}
		}
		return nil
	}); err != nil {
		return discovery, err
	}

	seen := make(map[string]struct{})
	for _, episode := range result.Episodes {
		if episode == nil {
			continue
		}
		if _, ok := seen[episode.ID]; ok {
			logger.WithFields(log.Fields{"episode_id": episode.ID, "title": episode.Title, "reason": model.ReasonDuplicateItem}).Debug("duplicate source item found")
			continue
		}
		seen[episode.ID] = struct{}{}
		if existing, ok := knownEpisodes[episode.ID]; ok {
			discovery.AlreadyKnown++
			logger.WithFields(itemLogFields(feedConfig, existing, model.ReasonMatchingGUIDExists, "matching source GUID already exists in cache", model.DecisionSourceCache)).Debug("Already known item")
		} else {
			discovery.NewItemsDiscovered++
			logger.WithFields(itemLogFields(feedConfig, episode, "discovered", "new source item discovered", model.DecisionSourceAutomatic)).Debug("Discovered item")
		}
	}

	if err := u.db.AddFeed(ctx, feedConfig.ID, result); err != nil {
		return discovery, err
	}

	if err := u.syncEpisodeMetadata(feedConfig.ID, result.Episodes); err != nil {
		return discovery, err
	}

	for _, episode := range result.Episodes {
		delete(episodeSet, episode.ID)
	}

	// removing episodes that are no longer available in the feed and not downloaded or cleaned
	for id := range episodeSet {
		logger.WithField("episode_id", id).Info("Episode is no longer present in the source feed and has no downloaded media; removing it from Podsync's pending cache")
		err := u.db.DeleteEpisode(feedConfig.ID, id)
		if err != nil {
			return discovery, err
		}
	}

	logger.Debug("successfully saved updates to storage")
	return discovery, nil
}

func (u *Manager) syncEpisodeMetadata(feedID string, episodes []*model.Episode) error {
	for _, episode := range episodes {
		if episode == nil || episode.ID == "" {
			continue
		}
		err := u.db.UpdateEpisode(feedID, episode.ID, func(existing *model.Episode) error {
			retroactiveOverlay := existing.MetadataSource != episode.MetadataSource && episode.MetadataSource == "rss"

			existing.Title = episode.Title
			existing.Description = episode.Description
			existing.Thumbnail = episode.Thumbnail
			existing.VideoURL = episode.VideoURL
			existing.Link = episode.Link
			existing.Author = episode.Author
			existing.Explicit = episode.Explicit
			existing.Order = episode.Order
			existing.OrderSource = episode.OrderSource
			existing.MetadataSource = episode.MetadataSource
			if !episode.PubDate.IsZero() {
				existing.PubDate = episode.PubDate
			}
			if existing.Duration <= 0 && episode.Duration > 0 {
				existing.Duration = episode.Duration
			}

			if retroactiveOverlay {
				log.WithFields(log.Fields{
					"feed_id":    feedID,
					"episode_id": episode.ID,
					"overlay":    episode.MetadataSource,
				}).Info("retroactive metadata update applied to existing episode")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (u *Manager) fetchEpisodes(ctx context.Context, feedConfig *feed.Config) ([]*model.Episode, error) {
	var (
		feedID       = feedConfig.ID
		downloadList []*model.Episode
		pageSize     = feedConfig.PageSize
	)

	logger := loggerWithExecution(ctx, log.Fields{"feed_id": feedID, "page_size": pageSize})
	logger.Info("fetching episodes for download")

	// Build the list of files to download
	err := u.db.WalkEpisodes(ctx, feedID, func(episode *model.Episode) error {
		var (
			logger = loggerWithExecution(ctx, log.Fields{"feed_id": feedID, "episode_id": episode.ID})
		)
		if episode.Status != model.EpisodeNew && episode.Status != model.EpisodeError && episode.Status != model.EpisodePlanned {
			// File already downloaded
			logger.WithField("status", episode.Status).Infof("Video Name: %q already has status %q, so Podsync will not download it again", episode.Title, episode.Status)
			return nil
		}

		filter := matchFiltersWithDecision(episode, &feedConfig.Filters)
		if !filter.Matched {
			_ = u.db.UpdateEpisode(feedID, episode.ID, func(existing *model.Episode) error {
				setEpisodeDecision(existing, filter.ReasonCode, filter.Reason, filter.Source, filter.Diagnostics)
				return nil
			})
			logger.WithFields(itemLogFields(feedConfig, episode, filter.ReasonCode, filter.Reason, filter.Source)).Debug("Excluded item")
			return nil
		}

		if strings.TrimSpace(episode.VideoURL) == "" {
			_ = u.db.UpdateEpisode(feedID, episode.ID, func(existing *model.Episode) error {
				setEpisodeDecision(existing, model.ReasonMissingMediaURL, "episode has no downloadable media URL", model.DecisionSourceAutomatic, nil)
				return nil
			})
			logger.WithFields(itemLogFields(feedConfig, episode, model.ReasonMissingMediaURL, "episode has no downloadable media URL", model.DecisionSourceAutomatic)).Debug("Skipped item")
			return nil
		}

		// Limit the number of episodes downloaded at once
		pageSize--
		if pageSize < 0 {
			logger.WithFields(log.Fields{"title": episode.Title, "page_size": feedConfig.PageSize}).Infof("Video Name: %q was not queued because this sync already reached the configured page_size limit of %d", episode.Title, feedConfig.PageSize)
			return nil
		}

		logger.WithField("title", episode.Title).Infof("Video Name: %q matched the feed rules and was queued for download", episode.Title)
		if err := u.db.UpdateEpisode(feedID, episode.ID, func(existing *model.Episode) error {
			existing.Status = model.EpisodePlanned
			return nil
		}); err != nil {
			return err
		}
		downloadList = append(downloadList, episode)
		return nil
	})

	if err != nil {
		return nil, errors.Wrapf(err, "failed to build update list")
	}

	return downloadList, nil
}

func (u *Manager) downloadEpisodes(ctx context.Context, feedConfig *feed.Config, downloadList []*model.Episode) (downloadSummary, error) {
	var (
		downloadCount = len(downloadList)
		summary       downloadSummary
		feedID        = feedConfig.ID
		hadFailures   bool
	)

	if downloadCount > 0 {
		loggerWithExecution(ctx, log.Fields{"feed_id": feedID, "download_count": downloadCount}).Infof("Podsync selected %d episode(s) from feed %q for download", downloadCount, feedID)
	} else {
		loggerWithExecution(ctx, log.Fields{"feed_id": feedID}).Infof("No new episodes from feed %q need to be downloaded", feedID)
		return summary, nil
	}

	// Download pending episodes

	for idx, episode := range downloadList {
		var (
			logger      = loggerWithExecution(ctx, log.Fields{"feed_id": feedID, "index": idx, "episode_id": episode.ID})
			episodeName = feed.EpisodeName(feedConfig, episode)
		)

		// Check whether episode already exists
		size, err := u.fs.Size(ctx, fmt.Sprintf("%s/%s", feedID, episodeName))
		if err == nil {
			logger.WithFields(itemLogFields(feedConfig, episode, model.ReasonAlreadyDownloaded, "matching media file already exists", model.DecisionSourceCache)).WithField("media_path", fmt.Sprintf("%s/%s", feedID, episodeName)).Debug("Reused existing item")

			// File already exists, update file status and disk size
			if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
				episode.Size = size
				episode.Status = model.EpisodeStored
				episode.LastError = ""
				episode.LastErrorAt = time.Time{}
				episode.FailureCategory = ""
				setEpisodeDecision(episode, model.ReasonAlreadyDownloaded, "matching media file already exists", model.DecisionSourceCache, map[string]string{"media_path": fmt.Sprintf("%s/%s", feedID, episodeName), "size_bytes": fmt.Sprint(size)})
				return nil
			}); err != nil {
				logger.WithError(err).Error("failed to update file info")
				return summary, err
			}

			summary.ReusedExistingMedia++
			continue
		} else if os.IsNotExist(err) {
			// Will download, do nothing here
		} else {
			logger.WithError(err).Error("failed to stat file")
			return summary, err
		}

		// Download episode to disk
		// We download the episode to a temp directory first to avoid downloading this file by clients
		// while still being processed by youtube-dl (e.g. a file is being downloaded from YT or encoding in progress)

		logger.Infof("Downloading Video Name: %q from %s", episode.Title, episode.VideoURL)
		if err := u.db.UpdateEpisode(feedID, episode.ID, func(existing *model.Episode) error {
			existing.Status = model.EpisodeDownloading
			setEpisodeDecision(existing, "downloading", "episode selected for download", model.DecisionSourceAutomatic, nil)
			return nil
		}); err != nil {
			return summary, err
		}
		tempFile, err := u.downloader.Download(ctx, feedConfig, episode)
		if err != nil {
			// YouTube might block host with HTTP Error 429: Too Many Requests
			// We still need to generate XML, so just stop sending download requests and
			// retry next time
			if err == ytdl.ErrTooManyRequests {
				logger.WithFields(itemLogFields(feedConfig, episode, model.ReasonDownloadFailed, "provider rate limit returned too many requests", model.DecisionSourceError)).Warn("Failed item")
				_ = u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
					episode.Status = model.EpisodeError
					episode.LastError = err.Error()
					episode.LastErrorAt = time.Now().UTC()
					episode.RetryCount++
					episode.FailureCategory = classifyFailure(err)
					setEpisodeDecision(episode, model.ReasonDownloadFailed, "provider rate limit returned too many requests", model.DecisionSourceError, map[string]string{"last_error": err.Error()})
					return nil
				})
				summary.Failed++
				hadFailures = true
				break
			}

			// Execute episode download error hooks
			if len(feedConfig.OnEpisodeDownloadError) > 0 {
				env := []string{
					"FEED_NAME=" + feedID,
					"EPISODE_TITLE=" + episode.Title,
					"ERROR_MESSAGE=" + err.Error(),
				}

				for i, hook := range feedConfig.OnEpisodeDownloadError {
					if hookErr := hook.Invoke(env); hookErr != nil {
						logger.Errorf("failed to execute episode download error hook %d: %v", i+1, hookErr)
					} else {
						logger.Infof("episode download error hook %d executed successfully", i+1)
					}
				}
			}

			if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
				episode.Status = model.EpisodeError
				episode.LastError = err.Error()
				episode.LastErrorAt = time.Now().UTC()
				episode.RetryCount++
				episode.FailureCategory = classifyFailure(err)
				setEpisodeDecision(episode, model.ReasonDownloadFailed, "episode download failed", model.DecisionSourceError, map[string]string{"last_error": err.Error(), "attempts": fmt.Sprint(episode.RetryCount)})
				return nil
			}); err != nil {
				return summary, err
			}
			logger.WithFields(itemLogFields(feedConfig, episode, model.ReasonDownloadFailed, "episode download failed", model.DecisionSourceError)).WithError(err).Debug("Failed item")
			hadFailures = true
			summary.Failed++

			continue
		}

		logger.Debug("copying file")
		if err := u.db.UpdateEpisode(feedID, episode.ID, func(existing *model.Episode) error {
			existing.Status = model.EpisodeProcessing
			return nil
		}); err != nil {
			tempFile.Close()
			return summary, err
		}
		trimmedReader, trimmedCleanup, err := u.trimEpisodeIfSignatureFound(ctx, feedConfig, episode, tempFile)
		if err != nil {
			tempFile.Close()
			logger.WithError(err).Error("signature trim failed")
			if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
				episode.Status = model.EpisodeError
				episode.LastError = err.Error()
				episode.LastErrorAt = time.Now().UTC()
				episode.RetryCount++
				episode.FailureCategory = model.FailureCategoryProcessing
				setEpisodeDecision(episode, model.ReasonMediaProbeFailed, "signature trim or media processing failed", model.DecisionSourceError, map[string]string{"last_error": err.Error()})
				return nil
			}); err != nil {
				return summary, err
			}
			hadFailures = true
			summary.Failed++
			continue
		}
		if trimmedCleanup != nil {
			defer trimmedCleanup()
		}
		processedDuration := episode.Duration
		if namedReader, ok := trimmedReader.(interface{ Name() string }); ok {
			if duration := resultDurationOrZero(ctx, namedReader.Name(), logger); duration > 0 {
				processedDuration = int64(duration.Seconds())
			}
		}
		publishResult, err := fs.NewPublisher(u.fs).Publish(ctx, fmt.Sprintf("%s/%s", feedID, episodeName), trimmedReader, fs.PublishOptions{MinSize: 1})
		tempFile.Close()
		if err != nil {
			if trimmedCleanup != nil {
				trimmedCleanup()
			}
			logger.WithError(err).Error("failed to copy file")
			_ = u.db.UpdateEpisode(feedID, episode.ID, func(existing *model.Episode) error {
				existing.Status = model.EpisodeError
				existing.LastError = err.Error()
				existing.LastErrorAt = time.Now().UTC()
				existing.RetryCount++
				existing.FailureCategory = model.FailureCategoryStorage
				setEpisodeDecision(existing, model.ReasonStorageWriteFailed, "failed to write media to storage", model.DecisionSourceError, map[string]string{"last_error": err.Error()})
				return nil
			})
			return summary, err
		}
		if trimmedCleanup != nil {
			trimmedCleanup()
		}

		// Execute post episode download hooks
		if len(feedConfig.PostEpisodeDownload) > 0 {
			env := []string{
				"EPISODE_FILE=" + fmt.Sprintf("%s/%s", feedID, episodeName),
				"FEED_NAME=" + feedID,
				"EPISODE_TITLE=" + episode.Title,
			}

			for i, hook := range feedConfig.PostEpisodeDownload {
				if err := hook.Invoke(env); err != nil {
					logger.Errorf("failed to execute post episode download hook %d: %v", i+1, err)
				} else {
					logger.Infof("post episode download hook %d executed successfully", i+1)
				}
			}
		}

		// Update file status in database

		logger.Infof("Downloaded Video Name: %q to %s", episode.Title, fmt.Sprintf("%s/%s", feedID, episodeName))
		if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
			episode.Size = publishResult.Size
			if processedDuration > 0 {
				episode.Duration = processedDuration
			}
			episode.Status = model.EpisodeStored
			episode.LastError = ""
			episode.LastErrorAt = time.Time{}
			episode.FailureCategory = ""
			setEpisodeDecision(episode, "downloaded", "episode media downloaded successfully", model.DecisionSourceAutomatic, map[string]string{"media_path": fmt.Sprintf("%s/%s", feedID, episodeName), "size_bytes": fmt.Sprint(publishResult.Size)})
			return nil
		}); err != nil {
			return summary, err
		}
		logger.WithFields(itemLogFields(feedConfig, episode, "downloaded", "episode media downloaded successfully", model.DecisionSourceAutomatic)).WithFields(log.Fields{"output": fmt.Sprintf("%s/%s", feedID, episodeName), "size": publishResult.Size, "duration_seconds": processedDuration}).Debug("Downloaded item")

		summary.Downloaded++
	}

	loggerWithExecution(ctx, log.Fields{"feed_id": feedID, "downloaded": summary.Downloaded, "reused_existing_media": summary.ReusedExistingMedia, "failed": summary.Failed}).Infof("Download stage completed for feed %q: downloaded=%d reused_existing_media=%d failed=%d", feedID, summary.Downloaded, summary.ReusedExistingMedia, summary.Failed)
	if hadFailures {
		_ = u.recordFeedRunFailure(ctx, feedID, errors.New("one or more episode downloads failed"))
	}
	return summary, nil
}

func (u *Manager) buildXML(ctx context.Context, feedConfig *feed.Config) error {
	return u.publicationService().PublishFeedXML(ctx, feedConfig)
}

func (u *Manager) buildOPML(ctx context.Context) error {
	return u.publicationService().PublishOPML(ctx)
}

func (u *Manager) publicationService() *PublicationService {
	if u.publication == nil {
		u.publication = NewPublicationService(u.db, u.fs, u.feeds, u.hostname)
	}
	return u.publication
}

func (u *Manager) BuildOPMLNow(ctx context.Context) error {
	return u.buildOPML(ctx)
}

func (u *Manager) FlushOPML(ctx context.Context) error {
	if u.opml == nil {
		return nil
	}
	return u.opml.Flush(ctx)
}

func (u *Manager) cleanup(ctx context.Context, feedConfig *feed.Config) error {
	var (
		feedID = feedConfig.ID
		logger = log.WithField("feed_id", feedID)
		list   []*model.Episode
		result *multierror.Error
	)

	if feedConfig.Clean == nil {
		logger.Debug("no cleanup policy configured")
		return nil
	}

	count := feedConfig.Clean.KeepLast
	if count < 1 {
		logger.Info("nothing to clean")
		return nil
	}

	logger.WithField("count", count).Info("running cleaner")
	if err := u.db.WalkEpisodes(ctx, feedConfig.ID, func(episode *model.Episode) error {
		if model.IsEpisodePublishable(episode.Status) {
			list = append(list, episode)
		}
		return nil
	}); err != nil {
		return err
	}

	if count > len(list) {
		return nil
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].PubDate.After(list[j].PubDate)
	})

	for _, episode := range list[count:] {
		logger.WithField("episode_id", episode.ID).Infof("deleting %q", episode.Title)

		var (
			episodeName = feed.EpisodeName(feedConfig, episode)
			path        = fmt.Sprintf("%s/%s", feedConfig.ID, episodeName)
		)

		err := u.fs.Delete(ctx, path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				logger.WithError(err).Errorf("failed to delete episode file: %s", episode.ID)
				result = multierror.Append(result, errors.Wrapf(err, "failed to delete episode: %s", episode.ID))
				continue
			}

			logger.WithField("episode_id", episode.ID).Info("episode was not found - file does not exist")
		}

		if err := u.db.UpdateEpisode(feedID, episode.ID, func(episode *model.Episode) error {
			episode.Status = model.EpisodeCleaned
			episode.Title = ""
			episode.Description = ""
			return nil
		}); err != nil {
			result = multierror.Append(result, errors.Wrapf(err, "failed to set state for cleaned episode: %s", episode.ID))
			continue
		}
	}

	return result.ErrorOrNil()
}

func (u *Manager) reconcileFeedState(ctx context.Context, feedConfig *feed.Config) error {
	logger := loggerWithExecution(ctx, log.Fields{"feed_id": feedConfig.ID})
	updated := 0
	err := u.db.WalkEpisodes(ctx, feedConfig.ID, func(episode *model.Episode) error {
		switch episode.Status {
		case model.EpisodePlanned, model.EpisodeDownloading, model.EpisodeProcessing, model.EpisodeStored:
			updated++
			return u.db.UpdateEpisode(feedConfig.ID, episode.ID, func(existing *model.Episode) error {
				existing.Status = model.EpisodeError
				existing.LastError = "recovered from interrupted update"
				existing.LastErrorAt = time.Now().UTC()
				existing.RetryCount++
				existing.FailureCategory = model.FailureCategoryUnknown
				setEpisodeDecision(existing, model.ReasonRecoveredInterruptedRun, "recovered from interrupted update", model.DecisionSourceError, nil)
				return nil
			})
		default:
			return nil
		}
	})
	if err != nil {
		return err
	}
	if updated > 0 {
		metricReconciledEpisodes.Add(int64(updated))
		logger.WithField("reconciled_episodes", updated).Warn("reconciled incomplete episode states before update")
	}
	return nil
}

func (u *Manager) countPublishableEpisodes(ctx context.Context, feedID string) int {
	count := 0
	_ = u.db.WalkEpisodes(ctx, feedID, func(episode *model.Episode) error {
		if model.IsEpisodePublishable(episode.Status) {
			count++
		}
		return nil
	})
	return count
}

func setEpisodeDecision(episode *model.Episode, reasonCode, reason, source string, diagnostics map[string]string) {
	episode.ReasonCode = reasonCode
	episode.Reason = reason
	episode.DecisionSource = source
	episode.ProcessedAt = time.Now().UTC()
	episode.Diagnostics = diagnostics
}

func itemLogFields(feedConfig *feed.Config, episode *model.Episode, reasonCode, reason, source string) log.Fields {
	fields := log.Fields{"feed_id": feedConfig.ID, "source_url": feedConfig.URL, "reason": reasonCode, "reason_message": reason, "decision_source": source}
	if episode != nil {
		fields["guid"] = episode.ID
		fields["title"] = episode.Title
		fields["published_at"] = episode.PubDate
		if episode.VideoURL != "" {
			fields["media_url"] = episode.VideoURL
		}
	}
	return fields
}

func logFeedSyncSummary(logger log.FieldLogger, summary *FeedSyncSummary) {
	logger.WithFields(log.Fields{
		"feed_id":               summary.FeedID,
		"feed_title":            summary.FeedTitle,
		"source_items_found":    summary.SourceItemsFound,
		"new_items_discovered":  summary.NewItemsDiscovered,
		"already_known":         summary.AlreadyKnown,
		"downloaded":            summary.Downloaded,
		"reused_existing_media": summary.ReusedExistingMedia,
		"skipped":               summary.Skipped,
		"excluded":              summary.Excluded,
		"failed":                summary.Failed,
		"generated_feed_items":  summary.GeneratedFeedItems,
		"duration":              summary.Duration,
	}).Info("Sync completed for feed")
}

func classifyFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ytdl.ErrTooManyRequests) {
		return model.FailureCategoryProvider
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout"), strings.Contains(message, "connection"), strings.Contains(message, "network"):
		return model.FailureCategoryNetwork
	case strings.Contains(message, "ffmpeg"), strings.Contains(message, "ffprobe"), strings.Contains(message, "signature"):
		return model.FailureCategoryProcessing
	default:
		return model.FailureCategoryUnknown
	}
}
