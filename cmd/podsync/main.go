package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jessevdk/go-flags"
	"github.com/mxpv/podsync/pkg/audiobookshelf"
	"github.com/mxpv/podsync/pkg/audiosig"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/services/update"
	"github.com/mxpv/podsync/services/web"
	"github.com/robfig/cron/v3"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/ytdl"
)

type Opts struct {
	ConfigPath  string `long:"config" short:"c" default:"config.toml" env:"PODSYNC_CONFIG_PATH" description:"Path to the configuration file"`
	Headless    bool   `long:"headless" description:"Run one update of every feed, then exit (no web server)"`
	Debug       bool   `long:"debug" description:"Enable debug logging"`
	NoBanner    bool   `long:"no-banner" description:"Do not print the startup banner"`
	CheckConfig bool   `long:"check-config" description:"Validate the configuration file and exit"`
	Init        bool   `long:"init" description:"Write a starter configuration file to the --config path and exit"`
}

const banner = `
 _______  _______  ______   _______           _        _______ 
(  ____ )(  ___  )(  __  \ (  ____ \|\     /|( (    /|(  ____ \
| (    )|| (   ) || (  \  )| (    \/( \   / )|  \  ( || (    \/
| (____)|| |   | || |   ) || (_____  \ (_) / |   \ | || |      
|  _____)| |   | || |   | |(_____  )  \   /  | (\ \) || |      
| (      | |   | || |   ) |      ) |   ) (   | | \   || |      
| )      | (___) || (__/  )/\____) |   | |   | )  \  || (____/\
|/       (_______)(______/ \_______)   \_/   |/    )_)(_______/
`

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
	arch    = ""
)

func main() {
	log.SetFormatter(&log.TextFormatter{
		TimestampFormat: time.RFC3339,
		FullTimestamp:   true,
	})

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Parse args
	opts := Opts{}
	_, err := flags.Parse(&opts)
	if err != nil {
		var flagsErr *flags.Error
		if errors.As(err, &flagsErr) && flagsErr.Type == flags.ErrHelp {
			return // help was printed
		}
		log.WithError(err).Fatal("failed to parse command line arguments")
	}

	if opts.Debug {
		log.SetLevel(log.DebugLevel)
	}

	if opts.Init {
		if err := writeStarterConfig(opts.ConfigPath); err != nil {
			exitWithError(fmt.Sprintf("failed to create starter configuration: %v", err))
		}
		fmt.Printf("Created starter configuration at %s\nAdd your feeds to it, then start Podsync.\n", absPath(opts.ConfigPath))
		return
	}

	if opts.CheckConfig {
		os.Exit(runCheckConfig(ctx, opts.ConfigPath))
	}

	if !opts.NoBanner {
		log.Info(banner)
	}

	log.WithFields(log.Fields{
		"version": version,
		"commit":  commit,
		"date":    date,
		"arch":    arch,
	}).Info("running podsync")

	// Load TOML file
	log.Debugf("loading configuration %q", opts.ConfigPath)
	cfg, created, err := loadStartupConfig(opts.ConfigPath)
	if err != nil {
		exitWithError(err.Error())
	}
	if created {
		log.Warnf("No configuration file was found, so a starter configuration was created at %s. Add your feeds to it and restart Podsync.", absPath(opts.ConfigPath))
		log.Warn("In Docker, mount the file (for example -v /path/to/config.toml:/app/config.toml) so your changes survive container restarts.")
	}
	if len(cfg.Feeds) == 0 {
		log.Warnf("No feeds are configured in %s. Podsync is running but has nothing to sync; add [feeds.<id>] sections and restart.", absPath(opts.ConfigPath))
	}

	if cfg.Log.Filename != "" {
		log.Infof("Using log file: %s", cfg.Log.Filename)

		log.SetOutput(&lumberjack.Logger{
			Filename:   cfg.Log.Filename,
			MaxSize:    cfg.Log.MaxSize,
			MaxBackups: cfg.Log.MaxBackups,
			MaxAge:     cfg.Log.MaxAge,
			Compress:   cfg.Log.Compress,
		})

		// Optionally enable debug mode from config.toml
		if cfg.Log.Debug {
			log.SetLevel(log.DebugLevel)
		}
	}

	if err := validateRuntimeDependencies(ctx, cfg); err != nil {
		log.WithError(err).Fatal("startup validation failed")
	}

	downloader, err := ytdl.New(ctx, cfg.Downloader)
	if err != nil {
		log.WithError(err).Fatal("youtube-dl error")
	}

	database, err := db.NewBadger(&cfg.Database)
	if err != nil {
		log.WithError(err).Fatal("failed to open database")
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.WithError(err).Error("failed to close database")
		}
	}()

	var storage fs.Storage
	switch cfg.Storage.Type {
	case "local":
		storage, err = fs.NewLocal(cfg.Storage.Local.DataDir, cfg.Server.WebUIEnabled)
	case "s3":
		storage, err = fs.NewS3(cfg.Storage.S3) // serving files from S3 is not supported, so no WebUI either
	default:
		log.Fatalf("unknown storage type: %s", cfg.Storage.Type)
	}
	if err != nil {
		log.WithError(err).Fatal("failed to open storage")
	}

	// Run updater thread
	log.Debug("creating key providers")
	keys := map[model.Provider]feed.KeyProvider{}
	for name, list := range cfg.Tokens {
		provider, err := feed.NewKeyProvider(list)
		if err != nil {
			log.WithError(err).Fatalf("failed to create key provider for %q", name)
		}
		keys[name] = provider
	}
	if _, ok := keys[model.ProviderRumble]; !ok {
		keys[model.ProviderRumble] = feed.NewStaticKeyProvider("")
	}

	log.Debug("creating update manager")
	manager, err := update.NewUpdater(cfg.Feeds, keys, cfg.Server.Hostname, cfg.Signatures.RootDir, downloader, database, storage)
	if err != nil {
		log.WithError(err).Fatal("failed to create updater")
	}
	manager.SetOPMLPublisher(update.NewOPMLPublisher(func(buildCtx context.Context) error {
		return manager.BuildOPMLNow(buildCtx)
	}, time.Second))
	if cfg.Audiobookshelf.Enabled {
		manager.SetAudiobookshelfExporter(audiobookshelf.NewExporter(cfg.Audiobookshelf.PodcastRoot))
		log.WithField("podcast_root", cfg.Audiobookshelf.PodcastRoot).Info("audiobookshelf hardlink export enabled")
	}

	// In Headless mode, do one round of feed updates and quit
	if opts.Headless {
		started := time.Now()
		var summaries []*update.FeedSyncSummary
		for _, _feed := range cfg.Feeds {
			summary, err := manager.UpdateWithSummary(ctx, _feed)
			if summary != nil {
				summaries = append(summaries, summary)
			}
			if err != nil {
				log.WithError(err).Errorf("failed to update feed: %s", _feed.URL)
			}
		}
		if err := manager.FlushOPML(ctx); err != nil {
			log.WithError(err).Error("failed to flush opml publisher")
		}
		logGlobalSyncSummary(summaries, time.Since(started))
		return
	}

	var scheduler *update.Scheduler
	group, ctx := errgroup.WithContext(ctx)
	defer func() {
		if err := manager.FlushOPML(context.Background()); err != nil {
			log.WithError(err).Error("failed to flush opml publisher")
		}
		log.Info("gracefully stopped")
	}()

	// Create Cron
	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)))
	m := make(map[string]cron.EntryID)
	scheduler = update.NewScheduler(manager, 4, 64)
	scheduler.Start(ctx)

	// Run cron scheduler
	group.Go(func() error {
		var cronID cron.EntryID

		for _, _feed := range cfg.Feeds {
			// Track if this feed has an explicit cron schedule
			hasExplicitCronSchedule := _feed.CronSchedule != ""

			if _feed.CronSchedule == "" {
				_feed.CronSchedule = fmt.Sprintf("@every %s", _feed.UpdatePeriod.String())
			}
			cronFeed := _feed
			if cronID, err = c.AddFunc(cronFeed.CronSchedule, func() {
				if !scheduler.Enqueue(cronFeed) {
					log.WithFields(log.Fields{
						"feed_id":     cronFeed.ID,
						"queue_stats": scheduler.Stats(),
					}).Debug("feed update request deduplicated")
					return
				}
				log.WithFields(log.Fields{
					"feed_id":     cronFeed.ID,
					"queue_stats": scheduler.Stats(),
				}).Debug("feed update requested")
			}); err != nil {
				log.WithError(err).Fatalf("can't create cron task for feed: %s", cronFeed.ID)
			}

			m[cronFeed.ID] = cronID
			log.Debugf("-> %s (update '%s')", cronFeed.ID, cronFeed.CronSchedule)

			// Only perform initial update if no explicit cron schedule is configured
			// This prevents unwanted updates when using fixed schedules in Docker deployments
			// If --no-banner is used (Docker default), still perform an initial update
			if !hasExplicitCronSchedule || opts.NoBanner {
				log.WithFields(log.Fields{
					"feed_id":               cronFeed.ID,
					"has_explicit_schedule": hasExplicitCronSchedule,
					"no_banner":             opts.NoBanner,
				}).Info("attempting startup enqueue")
				enqueued := scheduler.Enqueue(cronFeed)
				log.WithFields(log.Fields{
					"feed_id":  cronFeed.ID,
					"enqueued": enqueued,
				}).Info("startup enqueue result")
			}
		}

		c.Start()

		for {
			<-ctx.Done()

			log.Info("shutting down cron")
			c.Stop()

			return ctx.Err()
		}
	})

	if cfg.Storage.Type == "s3" {
		return // S3 content is hosted externally
	}

	// Run web server
	srv := web.New(cfg.Server, storage, database)
	srv.SetFeedCount(func() int { return len(cfg.Feeds) })

	group.Go(func() error {
		log.Infof("running listener at %s", srv.Addr)
		if cfg.Server.TLS {
			return srv.ListenAndServeTLS(cfg.Server.CertificatePath, cfg.Server.KeyFilePath)
		} else {
			return srv.ListenAndServe()
		}
	})

	group.Go(func() error {
		// Shutdown web server
		defer func() {
			ctxShutDown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer func() {
				cancel()
			}()
			log.Info("shutting down web server")
			if err := srv.Shutdown(ctxShutDown); err != nil {
				log.WithError(err).Error("server shutdown failed")
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-stop:
				cancel()
				return nil
			}
		}
	})

	if err := group.Wait(); err != nil && (err != context.Canceled && err != http.ErrServerClosed) {
		log.WithError(err).Error("wait error")
	}
	if scheduler != nil {
		scheduler.Stop()
	}
}

func logGlobalSyncSummary(summaries []*update.FeedSyncSummary, duration time.Duration) {
	fields := log.Fields{"feeds_processed": len(summaries), "duration": duration}
	for _, summary := range summaries {
		if summary == nil {
			continue
		}
		fields["source_items_found"] = fieldsInt(fields, "source_items_found") + summary.SourceItemsFound
		fields["new_items_discovered"] = fieldsInt(fields, "new_items_discovered") + summary.NewItemsDiscovered
		fields["downloaded"] = fieldsInt(fields, "downloaded") + summary.Downloaded
		fields["reused_existing_media"] = fieldsInt(fields, "reused_existing_media") + summary.ReusedExistingMedia
		fields["skipped"] = fieldsInt(fields, "skipped") + summary.Skipped
		fields["excluded"] = fieldsInt(fields, "excluded") + summary.Excluded
		fields["failed"] = fieldsInt(fields, "failed") + summary.Failed
	}
	log.WithFields(fields).Info("Podsync sync completed")
}

func fieldsInt(fields log.Fields, key string) int {
	if value, ok := fields[key].(int); ok {
		return value
	}
	return 0
}

// loadStartupConfig loads the configuration for a normal run. When the file does not exist, it
// writes a starter configuration (if the directory exists) and loads that instead.
func loadStartupConfig(path string) (*Config, bool, error) {
	cfg, err := LoadConfig(path)
	if err == nil || !errors.Is(err, ErrConfigNotFound) {
		if err != nil {
			return nil, false, fmt.Errorf("failed to load configuration: %w", err)
		}
		return cfg, false, nil
	}
	if writeErr := writeStarterConfig(path); writeErr != nil {
		return nil, false, fmt.Errorf("%s\n\nA starter configuration could not be created automatically: %v", missingConfigHelp(absPath(path)), writeErr)
	}
	cfg, err = LoadConfig(path)
	if err != nil {
		return nil, false, fmt.Errorf("failed to load generated starter configuration: %w", err)
	}
	return cfg, true, nil
}

// runCheckConfig validates the configuration and runtime dependencies without starting Podsync.
// It returns the process exit code.
func runCheckConfig(ctx context.Context, path string) int {
	cfg, err := LoadConfig(path)
	if err != nil {
		if errors.Is(err, ErrConfigNotFound) {
			fmt.Fprintln(os.Stderr, missingConfigHelp(absPath(path)))
		} else {
			fmt.Fprintf(os.Stderr, "Configuration is invalid: %v\n", err)
		}
		return 1
	}
	if err := validateRuntimeDependencies(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Configuration is valid, but a runtime dependency is missing: %v\n", err)
		return 1
	}
	fmt.Printf("Configuration OK: %s (%d feeds)\n", absPath(path), len(cfg.Feeds))
	return 0
}

// exitWithError prints a (possibly multi-line) message to stderr and exits. Unlike log.Fatal,
// it keeps line breaks readable.
func exitWithError(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func validateRuntimeDependencies(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if requiresSignatureTooling(cfg) {
		if err := audiosig.EnsureFFmpegAvailable(ctx); err != nil {
			return err
		}
		if _, err := exec.LookPath("ffprobe"); err != nil {
			return fmt.Errorf("ffprobe not found in PATH")
		}
	}
	return nil
}

func requiresSignatureTooling(cfg *Config) bool {
	if cfg == nil {
		return false
	}
	if strings.TrimSpace(cfg.Signatures.RootDir) != "" || strings.TrimSpace(os.Getenv("PODSYNC_SIGNATURES_DIR")) != "" {
		return true
	}
	sigRoot := cfg.signaturesRoot()
	for id, feedCfg := range cfg.Feeds {
		if feedCfg == nil {
			continue
		}
		if feedCfg.Custom.SponsorBlockConfig().Enabled || len(feedCfg.SignatureRules) > 0 {
			return true
		}
		// Signature trimming is active for any feed with a rules.json, including under the
		// default location in the local data directory.
		if sigRoot != "" {
			if _, err := os.Stat(update.SignatureRulesPath(sigRoot, id)); err == nil {
				return true
			}
		}
	}
	return false
}
