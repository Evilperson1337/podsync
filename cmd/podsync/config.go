package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/audiobookshelf"
	"github.com/mxpv/podsync/pkg/configschema"
	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/mxpv/podsync/pkg/ytdl"
	"github.com/mxpv/podsync/services/admin"
	"github.com/mxpv/podsync/services/update"
	"github.com/mxpv/podsync/services/web"
)

type Config struct {
	// Server is the web server configuration
	Server web.Config `toml:"server" doc:"Podcast web server: port, public hostname and TLS."`
	// S3 is the optional configuration for S3-compatible storage provider
	Storage fs.Config `toml:"storage" doc:"Where episodes and feeds are stored: local disk or S3-compatible storage."`
	// Log is the optional logging configuration
	Log Log `toml:"log" doc:"Optional log file and rotation. Logs go to stdout when no file is set."`
	// Database configuration
	Database db.Config `toml:"database" doc:"Metadata database location and tuning."`
	// Feeds is a list of feeds to host by this app.
	// ID will be used as feed ID in http://podsync.net/{FEED_ID}.xml
	Feeds map[string]*feed.Config `doc:"Podcast feeds, keyed by feed ID. Each feed is served at <hostname>/<ID>.xml."`
	// Tokens is API keys to use to access YouTube/Vimeo APIs.
	Tokens map[model.Provider]StringSlice `toml:"tokens" secret:"true" doc:"API keys per provider (youtube, vimeo, soundcloud, twitch). A list of keys is rotated."`
	// Downloader (youtube-dl) configuration
	Downloader ytdl.Config `toml:"downloader" doc:"youtube-dl / yt-dlp settings."`
	// Signatures configuration for optional audio signature trimming.
	Signatures SignatureConfig `toml:"signatures" doc:"Location of per-feed signature audio files used by signature_rules."`
	// Global cleanup policy applied to feeds that don't specify their own cleanup policy
	Cleanup *feed.Cleanup `toml:"cleanup" doc:"Default cleanup policy for feeds without their own clean setting."`
	// Audiobookshelf is the optional hardlink export into an Audiobookshelf podcast library
	Audiobookshelf audiobookshelf.Config `toml:"audiobookshelf" doc:"Hardlink episodes into an Audiobookshelf podcast library."`
	// Admin configures the authenticated admin interface
	Admin admin.Config `toml:"admin" doc:"Authenticated admin interface on its own port, for use behind a reverse proxy."`
}

type SignatureConfig struct {
	RootDir string `toml:"root_dir" doc:"Directory with per-feed signature folders (<root_dir>/<feed ID>/signatures). Defaults to the local data directory."`
}

type Log struct {
	// Filename to write the log to (instead of stdout)
	Filename string `toml:"filename" doc:"Write logs to this file instead of stdout."`
	// MaxSize is the maximum size of the log file in MB
	MaxSize int `toml:"max_size" doc:"Maximum log file size in MB before rotation (default 50)."`
	// MaxBackups is the maximum number of log file backups to keep after rotation
	MaxBackups int `toml:"max_backups" doc:"Number of rotated log files to keep (default 7)."`
	// MaxAge is the maximum number of days to keep the logs for
	MaxAge int `toml:"max_age" doc:"Days to keep rotated log files (default 30)."`
	// Compress old backups
	Compress bool `toml:"compress" doc:"Compress rotated log files."`
	// Debug mode
	Debug bool `toml:"debug" doc:"Enable debug logging."`
}

// ErrConfigNotFound is returned by LoadConfig when the configuration file does not exist.
var ErrConfigNotFound = errors.New("configuration file not found")

// LoadConfig loads configuration from a file path. The format (TOML, YAML or JSON) is chosen by
// the file extension, and PODSYNC__SECTION__KEY environment variables override file values.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.Wrapf(ErrConfigNotFound, "%s", path)
		}
		return nil, errors.Wrapf(err, "failed to read config file: %s", path)
	}
	return loadConfigData(path, data)
}

// loadConfigData loads configuration content as if it were the file at path: path picks the
// format and anchors path-relative defaults. It lets candidate configurations be validated
// without writing them.
func loadConfigData(path string, data []byte) (*Config, error) {
	format := configFormatFor(path)
	tree, err := parseConfigTree(format, data)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to parse %s as %s", path, strings.ToUpper(string(format)))
	}
	config := Config{}
	if err := applyConfigEnvOverrides(tree, reflect.TypeOf(config), os.Environ()); err != nil {
		return nil, err
	}
	if err := tree.Unmarshal(&config); err != nil {
		return nil, errors.Wrapf(err, "failed to decode %s", path)
	}
	if unknown := findUnknownConfigKeys(tree, reflect.TypeOf(config)); len(unknown) > 0 {
		keys := make([]string, 0, len(unknown))
		for _, key := range unknown {
			keys = append(keys, key.String())
		}
		return nil, errors.Errorf("unknown configuration keys in %s (check for typos or misplaced sections): %s", path, strings.Join(keys, ", "))
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

	for id, f := range c.Feeds {
		mergeFeedCustom(f)

		if f.URL == "" {
			result = multierror.Append(result, errors.Errorf("URL is required for %q", id))
		}

		if f.CronSchedule != "" {
			if _, err := cron.ParseStandard(f.CronSchedule); err != nil {
				result = multierror.Append(result, errors.Wrapf(err, "invalid cron_schedule %q for %q", f.CronSchedule, id))
			}
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

		if err := validateFeedAudiobookshelf(id, f.Audiobookshelf, c.Audiobookshelf.Enabled); err != nil {
			result = multierror.Append(result, err)
		}

		if err := c.validateSignatureRules(id, f); err != nil {
			result = multierror.Append(result, err)
		}
	}

	if err := c.validateAudiobookshelf(); err != nil {
		result = multierror.Append(result, err)
	}

	if err := c.validateAdmin(); err != nil {
		result = multierror.Append(result, err)
	}

	return result.ErrorOrNil()
}

// signaturesRoot resolves the signatures root the updater will use.
func (c *Config) signaturesRoot() string {
	localDataDir := ""
	if c.Storage.Type == "local" {
		localDataDir = c.Storage.Local.DataDir
	}
	return update.ResolveSignaturesRoot(c.Signatures.RootDir, localDataDir)
}

// validateSignatureRules fails on invalid signature_rules configured in TOML, including missing
// signature files. A legacy rules.json is checked too, but its problems are only logged so that
// existing deployments keep starting.
func (c *Config) validateSignatureRules(feedID string, f *feed.Config) error {
	root := c.signaturesRoot()
	rulesPath := ""
	if root != "" {
		rulesPath = update.SignatureRulesPath(root, feedID)
	}

	if len(f.SignatureRules) == 0 {
		if rulesPath != "" {
			warnRulesJSON(feedID, root, rulesPath)
		}
		return nil
	}

	if rulesPath != "" {
		if _, err := os.Stat(rulesPath); err == nil {
			log.Warnf("signature_rules are configured for %q, so %s is ignored", feedID, rulesPath)
		}
	}
	var result *multierror.Error
	for idx, rule := range f.SignatureRules {
		if err := checkSignatureRule(root, feedID, rule); err != nil {
			result = multierror.Append(result, errors.Wrapf(err, "signature_rules[%d] for %q", idx, feedID))
		}
	}
	return result.ErrorOrNil()
}

func warnRulesJSON(feedID, root, rulesPath string) {
	parsed, ok, err := update.ReadSignatureRules(rulesPath)
	if err != nil {
		log.WithError(err).Warnf("signature rules for %q in %s cannot be read; signature trimming will fail for this feed", feedID, rulesPath)
		return
	}
	if !ok {
		return
	}
	for idx, rule := range parsed.Rules {
		if err := checkSignatureRule(root, feedID, rule); err != nil {
			log.WithError(err).Warnf("rule %d in %s for %q will be skipped", idx, rulesPath, feedID)
		}
	}
}

// checkSignatureRule validates a rule's fields and that its signature file exists and is not empty.
func checkSignatureRule(root, feedID string, rule feed.SignatureRule) error {
	if err := rule.Validate(); err != nil {
		return err
	}
	path := update.SignatureFilePath(root, feedID, rule.File)
	if path == "" {
		return errors.Errorf("relative file %q needs [signatures] root_dir (or an absolute path) when not using local storage", rule.File)
	}
	info, err := os.Stat(path)
	if err != nil {
		return errors.Wrapf(err, "signature file %q is not accessible", path)
	}
	if info.IsDir() || info.Size() == 0 {
		return errors.Errorf("signature file %q is empty or not a file", path)
	}
	return nil
}

// validateAdmin checks the admin interface settings, including that it does not share the podcast
// server's port: the admin listener must stay separate so it can be protected on its own.
func (c *Config) validateAdmin() error {
	if !c.Admin.Enabled {
		return nil
	}
	if err := c.Admin.Validate(); err != nil {
		return err
	}
	serverPort := c.Server.Port
	if serverPort == 0 {
		serverPort = 8080
	}
	if c.Admin.Port == serverPort && c.Storage.Type != "s3" {
		return errors.Errorf("admin.port %d must differ from server.port; the admin interface needs its own listener", c.Admin.Port)
	}
	return nil
}

func (c *Config) validateAudiobookshelf() error {
	if !c.Audiobookshelf.Enabled {
		return nil
	}
	var result *multierror.Error
	if strings.TrimSpace(c.Audiobookshelf.PodcastRoot) == "" {
		result = multierror.Append(result, errors.New("audiobookshelf.podcast_root is required when audiobookshelf export is enabled"))
	}
	if c.Storage.Type != "local" {
		result = multierror.Append(result, errors.Errorf("audiobookshelf export requires local storage (hardlinks cannot be created from %q storage)", c.Storage.Type))
	}
	return result.ErrorOrNil()
}

func validateFeedAudiobookshelf(feedID string, cfg audiobookshelf.FeedConfig, globalEnabled bool) error {
	if !cfg.Enabled {
		return nil
	}
	if err := audiobookshelf.ValidateDirectory(cfg.Directory); err != nil {
		return errors.Wrapf(err, "invalid audiobookshelf.directory for %q", feedID)
	}
	if !globalEnabled {
		log.Warnf("audiobookshelf export is enabled for feed %q but disabled globally; set [audiobookshelf] enabled = true to export", feedID)
	}
	return nil
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

	c.Admin.ApplyDefaults()

	// Default local storage next to the config file, like the database directory. The deprecated
	// server.data_dir still takes precedence (see validate).
	if c.Storage.Type == "local" && c.Storage.Local.DataDir == "" && c.Server.DataDir == "" {
		c.Storage.Local.DataDir = filepath.Join(filepath.Dir(configPath), "data")
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

// legacyAPIKeyEnv maps providers to the environment variables that replace their API tokens.
var legacyAPIKeyEnv = map[model.Provider]string{
	model.ProviderYoutube:    "PODSYNC_YOUTUBE_API_KEY",
	model.ProviderVimeo:      "PODSYNC_VIMEO_API_KEY",
	model.ProviderSoundcloud: "PODSYNC_SOUNDCLOUD_API_KEY",
	model.ProviderTwitch:     "PODSYNC_TWITCH_API_KEY",
	model.ProviderRumble:     "PODSYNC_RUMBLE_API_KEY",
}

func (c *Config) applyEnv() {
	envVars := legacyAPIKeyEnv

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

// ConfigSchema describes StringSlice for the admin interface: a string or a list of strings.
func (StringSlice) ConfigSchema() configschema.Schema {
	return configschema.Schema{Type: []string{"string", "array"}, Items: &configschema.Schema{Type: "string"}}
}

func (s *StringSlice) UnmarshalTOML(v interface{}) error {
	// Trees built from YAML/JSON (toml.TreeFromMap) hold typed string lists.
	if list, ok := v.([]string); ok {
		*s = append([]string(nil), list...)
		return nil
	}
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
