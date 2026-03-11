package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jessevdk/go-flags"
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
	ConfigPath string `long:"config" short:"c" default:"config.toml" env:"PODSYNC_CONFIG_PATH"`
	Headless   bool   `long:"headless"`
	Debug      bool   `long:"debug"`
	NoBanner   bool   `long:"no-banner"`
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
		log.WithError(err).Fatal("failed to parse command line arguments")
	}

	if opts.Debug {
		log.SetLevel(log.DebugLevel)
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
	cfg, err := LoadConfig(opts.ConfigPath)
	if err != nil {
		log.WithError(err).Fatal("failed to load configuration file")
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

	// In Headless mode, do one round of feed updates and quit
	if opts.Headless {
		for _, _feed := range cfg.Feeds {
			if err := manager.Update(ctx, _feed); err != nil {
				log.WithError(err).Errorf("failed to update feed: %s", _feed.URL)
			}
		}
		if err := manager.FlushOPML(ctx); err != nil {
			log.WithError(err).Error("failed to flush opml publisher")
		}
		return
	}

	group, ctx := errgroup.WithContext(ctx)
	defer func() {
		if err := manager.FlushOPML(context.Background()); err != nil {
			log.WithError(err).Error("failed to flush opml publisher")
		}
		if err := group.Wait(); err != nil && (err != context.Canceled && err != http.ErrServerClosed) {
			log.WithError(err).Error("wait error")
		}
		log.Info("gracefully stopped")
	}()

	// Create Cron
	c := cron.New(cron.WithChain(cron.SkipIfStillRunning(cron.DiscardLogger)))
	m := make(map[string]cron.EntryID)
	scheduler := update.NewScheduler(manager, 4, 64)
	scheduler.Start(ctx)
	defer scheduler.Stop()

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
				scheduler.Enqueue(cronFeed)
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
	for _, feedCfg := range cfg.Feeds {
		if feedCfg != nil && feedCfg.Custom.SponsorBlockConfig().Enabled {
			return true
		}
	}
	return false
}
