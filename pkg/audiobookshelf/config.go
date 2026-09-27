package audiobookshelf

import (
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

// Config is the global Audiobookshelf export configuration.
//
//	[audiobookshelf]
//	enabled = true
//	podcast_root = "/data/media/podcasts"
type Config struct {
	// Enabled turns on hardlink export globally. Feeds must also opt in.
	Enabled bool `toml:"enabled"`
	// PodcastRoot is the Audiobookshelf podcast library root directory.
	PodcastRoot string `toml:"podcast_root"`
}

// FeedConfig is the per-feed Audiobookshelf export configuration.
//
//	[feeds.ID.audiobookshelf]
//	enabled = true
//	directory = "Doctrine"
type FeedConfig struct {
	// Enabled turns on hardlink export for this feed.
	Enabled bool `toml:"enabled"`
	// Directory is the podcast directory relative to PodcastRoot.
	Directory string `toml:"directory"`
}

// ValidateDirectory checks that a per-feed directory is a non-empty relative
// path that stays beneath the podcast root after normalization.
func ValidateDirectory(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("directory is required")
	}
	if filepath.IsAbs(dir) || filepath.VolumeName(dir) != "" || strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, `\`) {
		return errors.Errorf("directory %q must be relative to podcast_root", dir)
	}
	cleaned := filepath.Clean(dir)
	if cleaned == "." {
		return errors.Errorf("directory %q must name a subdirectory of podcast_root", dir)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return errors.Errorf("directory %q escapes podcast_root", dir)
	}
	return nil
}
