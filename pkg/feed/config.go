package feed

import (
	"time"

	"github.com/mxpv/podsync/pkg/audiobookshelf"
	"github.com/mxpv/podsync/pkg/model"
)

// Config is a configuration for a feed loaded from TOML
type Config struct {
	ID string `toml:"-"`
	// URL is a full URL of the field
	URL string `toml:"url" doc:"Channel, playlist, user or show URL (YouTube, Vimeo, SoundCloud, Twitch or Rumble)."`
	// PageSize is the number of pages to query from YouTube API.
	// NOTE: larger page sizes/often requests might drain your API token.
	PageSize int `toml:"page_size" doc:"Number of episodes checked per update (default 50). Large values use more API quota."`
	// UpdatePeriod is how often to check for updates.
	// Format is "300ms", "1.5h" or "2h45m".
	// Valid time units are "ns", "us" (or "µs"), "ms", "s", "m", "h".
	// NOTE: too often update check might drain your API token.
	UpdatePeriod time.Duration `toml:"update_period" doc:"How often to check for new episodes (default 6h). Ignored when cron_schedule is set."`
	// Cron expression format is how often to check update
	// NOTE: too often update check might drain your API token.
	CronSchedule string `toml:"cron_schedule" doc:"Cron expression for updates, e.g. \"0 */6 * * *\" or \"@every 12h\". Overrides update_period."`
	// Quality to use for this feed
	Quality model.Quality `toml:"quality" enum:"high,low" doc:"Download quality: \"high\" (default) or \"low\"."`
	// Maximum height of video
	MaxHeight int `toml:"max_height" doc:"Maximum video height in pixels, e.g. 720 or 1080."`
	// Format to use for this feed
	Format model.Format `toml:"format" enum:"audio,video,custom" doc:"Episode format: \"video\" (mp4, default), \"audio\" (mp3) or \"custom\" (see custom_format)."`
	// Custom format properties
	CustomFormat CustomFormat `toml:"custom_format" doc:"youtube-dl format and file extension, used when format is \"custom\"."`
	// Only download episodes that match the filters (defaults to matching anything)
	Filters Filters `toml:"filters" doc:"Only download episodes that match these filters."`
	// Clean is a cleanup policy to use for this feed
	Clean *Cleanup `toml:"clean" doc:"Cleanup policy for this feed. Overrides the global [cleanup] policy."`
	// Custom is a list of feed customizations
	Custom Custom `toml:"custom" doc:"Podcast metadata overrides and per-feed extras such as SponsorBlock."`
	// FeedCustom is an alias for Custom to support per-feed customization under feed_custom.
	FeedCustom Custom `toml:"feed_custom" doc:"Alternative name for custom. Values set here override custom."`
	// List of additional youtube-dl arguments passed at download time
	YouTubeDLArgs []string `toml:"youtube_dl_args" doc:"Extra youtube-dl arguments for this feed. Avoid --format, --output and --audio-format."`
	// Post episode download hooks - executed after each episode is successfully downloaded
	// Multiple hooks can be configured and will execute in sequence
	// Example:
	//   [[feeds.ID1.post_episode_download]]
	//   command = ["echo", "Downloaded: $EPISODE_TITLE"]
	//   timeout = 10
	PostEpisodeDownload []*ExecHook `toml:"post_episode_download" doc:"Commands run after each episode is downloaded. Environment: EPISODE_FILE, FEED_NAME, EPISODE_TITLE."`
	// Episode download error hooks - executed when an episode download fails
	// Available environment variables: FEED_NAME, EPISODE_TITLE, ERROR_MESSAGE
	// Multiple hooks can be configured and will execute in sequence
	// Example:
	//   [[feeds.ID1.on_episode_download_error]]
	//   command = ["curl", "-X", "POST", "-d", "Download failed: $ERROR_MESSAGE", "https://webhook.example.com/notify"]
	//   timeout = 30
	OnEpisodeDownloadError []*ExecHook `toml:"on_episode_download_error" doc:"Commands run when an episode download fails. Environment: FEED_NAME, EPISODE_TITLE, ERROR_MESSAGE."`
	// Included in OPML file
	OPML bool `toml:"opml" doc:"Include this feed in the OPML file (podsync.opml)."`
	// Private feed (not indexed by podcast aggregators)
	PrivateFeed bool `toml:"private_feed" doc:"Ask podcast directories not to index this feed."`
	// Playlist sort
	PlaylistSort model.Sorting `toml:"playlist_sort" enum:"asc,desc" doc:"Order to read playlist items: \"asc\" (default) or \"desc\" (from the end)."`
	// SignatureRules configures audio signature trimming for this feed. When set, it replaces
	// <signatures_root>/<feed_id>/signatures/rules.json.
	SignatureRules []SignatureRule `toml:"signature_rules" doc:"Audio signature trimming rules. Each rule finds a signature clip in episodes and trims around it."`
	// Audiobookshelf hardlink export for this feed (requires the global [audiobookshelf] section)
	Audiobookshelf audiobookshelf.FeedConfig `toml:"audiobookshelf" doc:"Hardlink this feed's episodes into an Audiobookshelf podcast directory (also requires [audiobookshelf] enabled)."`
}

type CustomFormat struct {
	YouTubeDLFormat string `toml:"youtube_dl_format" doc:"youtube-dl format selector, e.g. \"bestaudio[ext=m4a]\"."`
	Extension       string `toml:"extension" doc:"File extension of the result, e.g. \"m4a\"."`
}

type Filters struct {
	Title          string `toml:"title" doc:"Only download episodes whose title matches this regular expression."`
	NotTitle       string `toml:"not_title" doc:"Skip episodes whose title matches this regular expression."`
	Description    string `toml:"description" doc:"Only download episodes whose description matches this regular expression."`
	NotDescription string `toml:"not_description" doc:"Skip episodes whose description matches this regular expression."`
	MinDuration    int64  `toml:"min_duration" doc:"Minimum episode duration in seconds."`
	MaxDuration    int64  `toml:"max_duration" doc:"Maximum episode duration in seconds."`
	MaxAge         int    `toml:"max_age" doc:"Skip episodes older than this many days."`
	MinAge         int    `toml:"min_age" doc:"Skip episodes newer than this many days."`
	// More filters to be added here
}

type Custom struct {
	CoverArt               string        `toml:"cover_art" doc:"Podcast artwork URL, replacing the channel image."`
	CoverArtQuality        model.Quality `toml:"cover_art_quality" doc:"Channel artwork quality when cover_art is not set: \"high\" or \"low\"."`
	Category               string        `toml:"category" doc:"iTunes category, e.g. \"TV & Film\"."`
	Subcategories          []string      `toml:"subcategories" doc:"iTunes subcategories."`
	Explicit               bool          `toml:"explicit" doc:"Mark the podcast as explicit."`
	Language               string        `toml:"lang" doc:"Podcast language code, e.g. \"en\"."`
	Author                 string        `toml:"author" doc:"Podcast author."`
	Title                  string        `toml:"title" doc:"Podcast title, replacing the channel title."`
	Description            string        `toml:"description" doc:"Podcast description, replacing the channel description."`
	OwnerName              string        `toml:"ownerName" doc:"iTunes owner name."`
	OwnerEmail             string        `toml:"ownerEmail" doc:"iTunes owner email."`
	Link                   string        `toml:"link" doc:"Website link in the feed, replacing the source URL."`
	RSSMetadataURL         string        `toml:"rss_metadata_url" doc:"Official RSS feed of the show; its episode titles, descriptions and artwork are applied to matching episodes."`
	SponsorBlockEnabled    bool          `toml:"sponsorBlockEnabled" doc:"Legacy SponsorBlock switch; prefer sponsorblock.enabled."`
	SponsorBlockCategories []string      `toml:"sponsorBlockCategories" doc:"Legacy SponsorBlock categories; prefer sponsorblock.categories."`
	SponsorBlock           SponsorBlock  `toml:"sponsorblock" doc:"Remove SponsorBlock segments (YouTube and Rumble) from downloaded episodes."`
}

type SponsorBlock struct {
	Enabled    bool     `toml:"enabled" doc:"Trim SponsorBlock segments from episodes of this feed."`
	Categories []string `toml:"categories" doc:"Segment categories to remove, e.g. \"sponsor\", \"intro\", \"outro\", \"selfpromo\"."`
}

func (c Custom) SponsorBlockConfig() SponsorBlock {
	if c.SponsorBlock.Enabled || c.SponsorBlock.Categories != nil {
		return c.SponsorBlock
	}
	return SponsorBlock{
		Enabled:    c.SponsorBlockEnabled,
		Categories: c.SponsorBlockCategories,
	}
}

type Cleanup struct {
	// KeepLast defines how many episodes to keep
	KeepLast int `toml:"keep_last" doc:"Number of most recent episodes to keep; older episodes are deleted."`
}
