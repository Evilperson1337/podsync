package model

import (
	"time"
)

// Quality to use when downloading episodes
type Quality string

const (
	QualityHigh = Quality("high")
	QualityLow  = Quality("low")
)

// Format to convert episode when downloading episodes
type Format string

const (
	FormatAudio  = Format("audio")
	FormatVideo  = Format("video")
	FormatCustom = Format("custom")
)

// Playlist sorting style
type Sorting string

const (
	SortingDesc = Sorting("desc")
	SortingAsc  = Sorting("asc")
)

type Episode struct {
	// ID of episode
	ID              string        `json:"id"`
	Title           string        `json:"title"`
	Subtitle        string        `json:"subtitle,omitempty"`
	Description     string        `json:"description"`
	Summary         string        `json:"summary,omitempty"`
	Thumbnail       string        `json:"thumbnail"`
	Link            string        `json:"link,omitempty"`
	Author          string        `json:"author,omitempty"`
	Keywords        string        `json:"keywords,omitempty"`
	Explicit        *bool         `json:"explicit,omitempty"`
	Duration        int64         `json:"duration"`
	VideoURL        string        `json:"video_url"`
	PubDate         time.Time     `json:"pub_date"`
	Size            int64         `json:"size"`
	Season          int           `json:"season,omitempty"`
	EpisodeNumber   int           `json:"episode_number,omitempty"`
	EpisodeType     string        `json:"episode_type,omitempty"`
	Order           string        `json:"order"`
	OrderSource     string        `json:"order_source,omitempty"`
	MetadataSource  string        `json:"metadata_source,omitempty"`
	Status          EpisodeStatus `json:"status"` // Workflow status
	LastError       string        `json:"last_error,omitempty"`
	LastErrorAt     time.Time     `json:"last_error_at,omitempty"`
	RetryCount      int           `json:"retry_count,omitempty"`
	FailureCategory string        `json:"failure_category,omitempty"`
}

type Feed struct {
	ID              string     `json:"feed_id"`
	ItemID          string     `json:"item_id"`
	LinkType        Type       `json:"link_type"` // Either group, channel or user
	Provider        Provider   `json:"provider"`  // Youtube or Vimeo
	CreatedAt       time.Time  `json:"created_at"`
	LastAccess      time.Time  `json:"last_access"`
	ExpirationTime  time.Time  `json:"expiration_time"`
	Format          Format     `json:"format"`
	Quality         Quality    `json:"quality"`
	CoverArtQuality Quality    `json:"cover_art_quality"`
	PageSize        int        `json:"page_size"`
	CoverArt        string     `json:"cover_art"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	PubDate         time.Time  `json:"pub_date"`
	Author          string     `json:"author"`
	ItemURL         string     `json:"item_url"` // Platform specific URL
	Episodes        []*Episode `json:"-"`        // Array of episodes
	UpdatedAt       time.Time  `json:"updated_at"`
	LastSuccessAt   time.Time  `json:"last_success_at,omitempty"`
	LastFailureAt   time.Time  `json:"last_failure_at,omitempty"`
	LastFailure     string     `json:"last_failure,omitempty"`
	PlaylistSort    Sorting    `json:"playlist_sort"`
	PrivateFeed     bool       `json:"private_feed"`
}

type EpisodeStatus string

const (
	EpisodeNew         = EpisodeStatus("new")         // New episode received via API
	EpisodePlanned     = EpisodeStatus("planned")     // Selected for processing in the current run
	EpisodeDownloading = EpisodeStatus("downloading") // Download in progress
	EpisodeProcessing  = EpisodeStatus("processing")  // Local processing in progress
	EpisodeStored      = EpisodeStatus("stored")      // Media stored, not yet reflected in XML publication
	EpisodePublished   = EpisodeStatus("published")   // Media stored and included in generated XML
	EpisodeDownloaded  = EpisodeStatus("downloaded")  // Legacy persisted state treated as publishable
	EpisodeError       = EpisodeStatus("error")       // Could not download/process, will retry
	EpisodeCleaned     = EpisodeStatus("cleaned")     // Downloaded and later removed from disk due to update strategy
)

const (
	FailureCategoryProvider   = "provider"
	FailureCategoryNetwork    = "network"
	FailureCategoryProcessing = "processing"
	FailureCategoryStorage    = "storage"
	FailureCategoryHook       = "hook"
	FailureCategoryUnknown    = "unknown"
)

func IsEpisodePublishable(status EpisodeStatus) bool {
	switch status {
	case EpisodeStored, EpisodePublished, EpisodeDownloaded:
		return true
	default:
		return false
	}
}
