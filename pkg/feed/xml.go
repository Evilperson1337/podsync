package feed

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	itunes "github.com/eduncan911/podcast"
	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/model"
)

// sort.Interface implementation
type timeSlice []*model.Episode

func (p timeSlice) Len() int {
	return len(p)
}

// In descending order
func (p timeSlice) Less(i, j int) bool {
	return p[i].PubDate.After(p[j].PubDate)
}

func (p timeSlice) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

func shouldUseEpisodeOrder(episodes []*model.Episode) bool {
	for _, episode := range episodes {
		if episode == nil {
			continue
		}
		if episode.OrderSource != "" && episode.Order != "" {
			return true
		}
	}
	return false
}

func parseEpisodeOrder(order string) (int, bool) {
	value, err := strconv.Atoi(order)
	if err != nil {
		return 0, false
	}
	return value, true
}

func sortEpisodesForXML(episodes []*model.Episode) {
	if shouldUseEpisodeOrder(episodes) {
		sort.SliceStable(episodes, func(i, j int) bool {
			leftOrder, leftOK := parseEpisodeOrder(episodes[i].Order)
			rightOrder, rightOK := parseEpisodeOrder(episodes[j].Order)

			switch {
			case leftOK && rightOK && leftOrder != rightOrder:
				return leftOrder < rightOrder
			case leftOK != rightOK:
				return leftOK
			}

			if !episodes[i].PubDate.Equal(episodes[j].PubDate) {
				return episodes[i].PubDate.After(episodes[j].PubDate)
			}

			return episodes[i].ID < episodes[j].ID
		})
		return
	}

	sort.Sort(timeSlice(episodes))
}

func Build(_ctx context.Context, feed *model.Feed, cfg *Config, hostname string) (*itunes.Podcast, error) {
	const (
		podsyncGenerator = "Podsync generator (support us at https://github.com/mxpv/podsync)"
		defaultCategory  = "TV & Film"
	)

	var (
		now         = time.Now().UTC()
		author      = feed.Author
		title       = feed.Title
		description = feed.Description
		feedLink    = feed.ItemURL
	)

	if author == "<notfound>" {
		author = feed.Title
	}

	if cfg.Custom.Author != "" {
		author = cfg.Custom.Author
	}

	if cfg.Custom.Title != "" {
		title = cfg.Custom.Title
	}

	if cfg.Custom.Description != "" {
		description = cfg.Custom.Description
	}

	if cfg.Custom.Link != "" {
		feedLink = cfg.Custom.Link
	}

	p := itunes.New(title, feedLink, description, &feed.PubDate, &now)
	p.Generator = podsyncGenerator
	p.AddSubTitle(title)
	p.IAuthor = author
	p.AddSummary(description)

	if feed.PrivateFeed {
		p.IBlock = "yes"
	}

	if cfg.Custom.OwnerName != "" && cfg.Custom.OwnerEmail != "" {
		p.IOwner = &itunes.Author{
			Name:  cfg.Custom.OwnerName,
			Email: cfg.Custom.OwnerEmail,
		}
	}

	if cfg.Custom.CoverArt != "" {
		p.AddImage(cfg.Custom.CoverArt)
	} else {
		p.AddImage(feed.CoverArt)
	}

	if cfg.Custom.Category != "" {
		p.AddCategory(cfg.Custom.Category, cfg.Custom.Subcategories)
	} else {
		p.AddCategory(defaultCategory, cfg.Custom.Subcategories)
	}

	if cfg.Custom.Explicit {
		p.IExplicit = "true"
	} else {
		p.IExplicit = "false"
	}

	if cfg.Custom.Language != "" {
		p.Language = cfg.Custom.Language
	}

	for _, episode := range feed.Episodes {
		if episode.PubDate.IsZero() {
			episode.PubDate = now
		}
	}

	// Sort episodes by overlay-provided ordering when present, otherwise keep
	// the historical publish-date ordering.
	sortEpisodesForXML(feed.Episodes)

	for i, episode := range feed.Episodes {
		if episode.Status != model.EpisodeDownloaded {
			// Skip episodes that are not yet downloaded or have been removed
			continue
		}

		item := itunes.Item{
			GUID:        episode.ID,
			Link:        episode.VideoURL,
			Title:       episode.Title,
			Description: episode.Description,
			ISubtitle:   episode.Title,
			// Some app prefer 1-based order
			IOrder: strconv.Itoa(i + 1),
		}

		if episode.Link != "" {
			item.Link = episode.Link
		}

		item.AddPubDate(&episode.PubDate)
		item.AddSummary(episode.Description)
		item.AddImage(episode.Thumbnail)
		item.AddDuration(episode.Duration)
		if episode.Author != "" {
			item.IAuthor = episode.Author
			item.AuthorFormatted = episode.Author
		}

		enclosureType := itunes.MP4
		if feed.Format == model.FormatAudio {
			enclosureType = itunes.MP3
		}
		if feed.Format == model.FormatCustom {
			enclosureType = EnclosureFromExtension(cfg)
		}

		var (
			episodeName = EpisodeName(cfg, episode)
			downloadURL = fmt.Sprintf("%s/%s/%s", strings.TrimRight(hostname, "/"), cfg.ID, episodeName)
		)

		item.AddEnclosure(downloadURL, enclosureType, episode.Size)

		// p.AddItem requires description to be not empty, use workaround
		if item.Description == "" {
			item.Description = " "
		}

		explicit := cfg.Custom.Explicit
		if episode.Explicit != nil {
			explicit = *episode.Explicit
		}

		if explicit {
			item.IExplicit = "true"
		} else {
			item.IExplicit = "false"
		}

		if _, err := p.AddItem(item); err != nil {
			return nil, errors.Wrapf(err, "failed to add item to podcast (id %q)", episode.ID)
		}
	}

	return &p, nil
}

func EpisodeName(feedConfig *Config, episode *model.Episode) string {
	ext := "mp4"
	if feedConfig.Format == model.FormatAudio {
		ext = "mp3"
	}
	if feedConfig.Format == model.FormatCustom {
		ext = feedConfig.CustomFormat.Extension
	}

	return fmt.Sprintf("%s.%s", episode.ID, ext)
}

func EnclosureFromExtension(feedConfig *Config) itunes.EnclosureType {
	ext := feedConfig.CustomFormat.Extension

	switch ext {
	case "m4a":
		return itunes.M4A
	case "m4v":
		return itunes.M4V
	case "mp4":
		return itunes.MP4
	case "mp3":
		return itunes.MP3
	case "mov":
		return itunes.MOV
	case "pdf":
		return itunes.PDF
	case "epub":
		return itunes.EPUB
	default:
		return -1
	}
}
