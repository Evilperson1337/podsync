package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/ytdl"
	"github.com/mxpv/podsync/services/web"
)

type Config struct {
	// Server is the web server configuration
	Server web.Config `toml:"server"`
	// S3 is the optional configuration for S3-compatible storage provider
	Storage fs.Config `toml:"storage"`
	// Log is the optional logging configuration
	Log Log `toml:"log"`
	// Database configuration
	Database db.Config `toml:"database"`
	// Feeds is a list of feeds to host by this app.
	// ID will be used as feed ID in http://podsync.net/{FEED_ID}.xml
	Feeds map[string]*feed.Config
	// Tokens is API keys to use to access YouTube/Vimeo APIs.
	Tokens map[model.Provider]StringSlice `toml:"tokens"`
	// Downloader (youtube-dl) configuration
	Downloader ytdl.Config `toml:"downloader"`
	// Signatures configuration for optional audio signature trimming.
	Signatures SignatureConfig `toml:"signatures"`
	// Global cleanup policy applied to feeds that don't specify their own cleanup policy
	Cleanup *feed.Cleanup `toml:"cleanup"`
}

type SignatureConfig struct {
	RootDir string `toml:"root_dir"`
}

type Log struct {
	// Filename to write the log to (instead of stdout)
	Filename string `toml:"filename"`
	// MaxSize is the maximum size of the log file in MB
	MaxSize int `toml:"max_size"`
	// MaxBackups is the maximum number of log file backups to keep after rotation
	MaxBackups int `toml:"max_backups"`
	// MaxAge is the maximum number of days to keep the logs for
	MaxAge int `toml:"max_age"`
	// Compress old backups
	Compress bool `toml:"compress"`
	// Debug mode
	Debug bool `toml:"debug"`
}

// LoadConfig loads TOML configuration from a file path
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read config file: %s", path)
	}

	config := Config{}
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal toml")
	}

	for id, f := range config.Feeds {
		f.ID = id
	}

	config.applyDefaults(path)
	config.applyEnv()

	if err := config.validate(); err != nil {
		return nil, err
	}

	return &config, nil
}

func (c *Config) validate() error {
	var result *multierror.Error

	if c.Server.DataDir != "" {
		log.Warnf(`server.data_dir is deprecated, and will be removed in a future release. Use the following config instead:

[storage]
  [storage.local]
  data_dir = "%s"

`, c.Server.DataDir)
		if c.Storage.Local.DataDir == "" {
			c.Storage.Local.DataDir = c.Server.DataDir
		}
	}

	if c.Server.Path != "" {
		var pathReg = regexp.MustCompile(model.PathRegex)
		if !pathReg.MatchString(c.Server.Path) {
			result = multierror.Append(result, errors.Errorf("Server handle path must be match %s or empty", model.PathRegex))
		}
	}

	switch c.Storage.Type {
	case "local":
		if c.Storage.Local.DataDir == "" {
			result = multierror.Append(result, errors.New("data directory is required for local storage"))
		}
	case "s3":
		if c.Storage.S3.EndpointURL == "" || c.Storage.S3.Region == "" || c.Storage.S3.Bucket == "" {
			result = multierror.Append(result, errors.New("S3 storage requires endpoint_url, region and bucket to be set"))
		}
		if strings.Contains(c.Server.Hostname, "localhost") || strings.Contains(c.Server.Hostname, "127.0.0.1") {
			result = multierror.Append(result, errors.New("server.hostname must be externally reachable when using S3 storage"))
		}
	default:
		result = multierror.Append(result, errors.Errorf("unknown storage type: %s", c.Storage.Type))
	}

	if len(c.Feeds) == 0 {
		result = multierror.Append(result, errors.New("at least one feed must be specified"))
	}

	for id, f := range c.Feeds {
		mergeFeedCustom(f)

		if f.URL == "" {
			result = multierror.Append(result, errors.Errorf("URL is required for %q", id))
		}

		if err := validateCustomFormat(id, f); err != nil {
			result = multierror.Append(result, err)
		}
		if err := validateHooks(id, f.PostEpisodeDownload, "post_episode_download"); err != nil {
			result = multierror.Append(result, err)
		}
		if err := validateHooks(id, f.OnEpisodeDownloadError, "on_episode_download_error"); err != nil {
			result = multierror.Append(result, err)
		}

		if rssURL := strings.TrimSpace(f.Custom.RSSMetadataURL); rssURL != "" {
			parsed, err := url.ParseRequestURI(rssURL)
			if err != nil {
				result = multierror.Append(result, errors.Wrapf(err, "invalid rss_metadata_url for %q", id))
				continue
			}

			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				result = multierror.Append(result, errors.Errorf("rss_metadata_url for %q must use http or https", id))
			}
		}

		if err := validateSponsorBlockConfig(id, f.Custom.SponsorBlockConfig()); err != nil {
			result = multierror.Append(result, err)
		}
	}

	return result.ErrorOrNil()
}

func (c *Config) applyDefaults(configPath string) {
	if c.Server.Hostname == "" {
		if c.Server.Port != 0 && c.Server.Port != 80 {
			c.Server.Hostname = fmt.Sprintf("http://localhost:%d", c.Server.Port)
		} else {
			c.Server.Hostname = "http://localhost"
		}
	}

	if c.Storage.Type == "" {
		c.Storage.Type = "local"
	}

	if c.Log.Filename != "" {
		if c.Log.MaxSize == 0 {
			c.Log.MaxSize = model.DefaultLogMaxSize
		}
		if c.Log.MaxAge == 0 {
			c.Log.MaxAge = model.DefaultLogMaxAge
		}
		if c.Log.MaxBackups == 0 {
			c.Log.MaxBackups = model.DefaultLogMaxBackups
		}
	}

	if c.Database.Dir == "" {
		c.Database.Dir = filepath.Join(filepath.Dir(configPath), "db")
	}

	for _, _feed := range c.Feeds {
		mergeFeedCustom(_feed)

		if _feed.UpdatePeriod == 0 {
			_feed.UpdatePeriod = model.DefaultUpdatePeriod
		}

		if _feed.Quality == "" {
			_feed.Quality = model.DefaultQuality
		}

		if _feed.Custom.CoverArtQuality == "" {
			_feed.Custom.CoverArtQuality = model.DefaultQuality
		}

		if _feed.Format == "" {
			_feed.Format = model.DefaultFormat
		}

		if _feed.PageSize == 0 {
			_feed.PageSize = model.DefaultPageSize
		}

		if _feed.PlaylistSort == "" {
			_feed.PlaylistSort = model.SortingAsc
		}

		// Apply global cleanup policy if feed doesn't have its own
		if _feed.Clean == nil && c.Cleanup != nil {
			_feed.Clean = c.Cleanup
		}
	}
}

func mergeFeedCustom(cfg *feed.Config) {
	if cfg == nil {
		return
	}
	if isCustomZero(cfg.FeedCustom) {
		return
	}
	merged := cfg.Custom
	if cfg.FeedCustom.CoverArt != "" {
		merged.CoverArt = cfg.FeedCustom.CoverArt
	}
	if cfg.FeedCustom.CoverArtQuality != "" {
		merged.CoverArtQuality = cfg.FeedCustom.CoverArtQuality
	}
	if cfg.FeedCustom.Category != "" {
		merged.Category = cfg.FeedCustom.Category
	}
	if cfg.FeedCustom.Subcategories != nil {
		merged.Subcategories = cfg.FeedCustom.Subcategories
	}
	if cfg.FeedCustom.Explicit {
		merged.Explicit = true
	}
	if cfg.FeedCustom.Language != "" {
		merged.Language = cfg.FeedCustom.Language
	}
	if cfg.FeedCustom.Author != "" {
		merged.Author = cfg.FeedCustom.Author
	}
	if cfg.FeedCustom.Title != "" {
		merged.Title = cfg.FeedCustom.Title
	}
	if cfg.FeedCustom.Description != "" {
		merged.Description = cfg.FeedCustom.Description
	}
	if cfg.FeedCustom.OwnerName != "" {
		merged.OwnerName = cfg.FeedCustom.OwnerName
	}
	if cfg.FeedCustom.OwnerEmail != "" {
		merged.OwnerEmail = cfg.FeedCustom.OwnerEmail
	}
	if cfg.FeedCustom.Link != "" {
		merged.Link = cfg.FeedCustom.Link
	}
	if cfg.FeedCustom.RSSMetadataURL != "" {
		merged.RSSMetadataURL = cfg.FeedCustom.RSSMetadataURL
	}
	if cfg.FeedCustom.SponsorBlockEnabled {
		merged.SponsorBlockEnabled = true
	}
	if cfg.FeedCustom.SponsorBlockCategories != nil {
		merged.SponsorBlockCategories = cfg.FeedCustom.SponsorBlockCategories
	}
	if cfg.FeedCustom.SponsorBlock.Enabled || cfg.FeedCustom.SponsorBlock.Categories != nil {
		merged.SponsorBlock = cfg.FeedCustom.SponsorBlock
	}
	cfg.Custom = merged
}

func isCustomZero(cfg feed.Custom) bool {
	return cfg.CoverArt == "" &&
		cfg.CoverArtQuality == "" &&
		cfg.Category == "" &&
		len(cfg.Subcategories) == 0 &&
		!cfg.Explicit &&
		cfg.Language == "" &&
		cfg.Author == "" &&
		cfg.Title == "" &&
		cfg.Description == "" &&
		cfg.OwnerName == "" &&
		cfg.OwnerEmail == "" &&
		cfg.Link == "" &&
		cfg.RSSMetadataURL == "" &&
		!cfg.SponsorBlockEnabled &&
		len(cfg.SponsorBlockCategories) == 0 &&
		!cfg.SponsorBlock.Enabled &&
		len(cfg.SponsorBlock.Categories) == 0
}

func validateSponsorBlockConfig(feedID string, cfg feed.SponsorBlock) error {
	for _, category := range cfg.Categories {
		if !slices.Contains(feed.ValidSponsorBlockCategories(), category) {
			return errors.Errorf("invalid sponsorblock category %q for %q", category, feedID)
		}
	}
	return nil
}

func validateCustomFormat(feedID string, cfg *feed.Config) error {
	if cfg.Format != model.FormatCustom {
		return nil
	}
	var result *multierror.Error
	if strings.TrimSpace(cfg.CustomFormat.Extension) == "" {
		result = multierror.Append(result, errors.Errorf("custom_format.extension is required for %q when format=custom", feedID))
	}
	if strings.TrimSpace(cfg.CustomFormat.YouTubeDLFormat) == "" {
		result = multierror.Append(result, errors.Errorf("custom_format.youtube_dl_format is required for %q when format=custom", feedID))
	}
	return result.ErrorOrNil()
}

func validateHooks(feedID string, hooks []*feed.ExecHook, field string) error {
	for idx, hook := range hooks {
		if hook == nil {
			return errors.Errorf("%s[%d] for %q cannot be nil", field, idx, feedID)
		}
		if len(hook.Command) == 0 {
			return errors.Errorf("%s[%d] for %q must define command", field, idx, feedID)
		}
		if hook.Timeout < 0 {
			return errors.Errorf("%s[%d] for %q timeout must be non-negative", field, idx, feedID)
		}
		switch strings.ToLower(strings.TrimSpace(hook.Shell)) {
		case "", "none", "cmd", "powershell", "pwsh":
		case "sh":
			if runtime.GOOS == "windows" {
				return errors.Errorf("%s[%d] for %q cannot use shell=sh on Windows", field, idx, feedID)
			}
		default:
			return errors.Errorf("%s[%d] for %q uses unsupported shell %q", field, idx, feedID, hook.Shell)
		}
	}
	return nil
}

func (c *Config) applyEnv() {
	envVars := map[model.Provider]string{
		model.ProviderYoutube:    "PODSYNC_YOUTUBE_API_KEY",
		model.ProviderVimeo:      "PODSYNC_VIMEO_API_KEY",
		model.ProviderSoundcloud: "PODSYNC_SOUNDCLOUD_API_KEY",
		model.ProviderTwitch:     "PODSYNC_TWITCH_API_KEY",
		model.ProviderRumble:     "PODSYNC_RUMBLE_API_KEY",
	}

	// Replace API keys from config with environment variables
	for provider, envVar := range envVars {
		val, ok := os.LookupEnv(envVar)
		if ok {
			log.Infof("Found %s environment variable, replacing config token with it", envVar)
			// If no tokens are provided in the config.toml, we need to create a new map
			if c.Tokens == nil {
				c.Tokens = make(map[model.Provider]StringSlice)
			}
			// Support multiple keys separated by spaces for API key rotation
			keys := strings.Fields(val)
			c.Tokens[provider] = keys
		}
	}
}

// StringSlice is a toml extension that lets you to specify either a string
// value (a slice with just one element) or a string slice.
type StringSlice []string

func (s *StringSlice) UnmarshalTOML(v interface{}) error {
	if list, ok := v.([]interface{}); ok {
		result := make([]string, 0, len(list))
		for _, entry := range list {
			value, ok := entry.(string)
			if !ok {
				return errors.New("failed to decode string slice field")
			}
			result = append(result, value)
		}
		*s = result
		return nil
	}

	if str, ok := v.(string); ok {
		*s = []string{str}
		return nil
	}

	return errors.New("failed to decode string slice field")
}
