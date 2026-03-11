package feed

import (
	"context"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	itunes "github.com/eduncan911/podcast"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

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
		logger      = log.WithFields(log.Fields{"feed_id": cfg.ID})
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
		if !model.IsEpisodePublishable(episode.Status) {
			// Skip episodes that are not yet downloaded or have been removed
			continue
		}

		item := itunes.Item{
			GUID:        episode.ID,
			Link:        episode.VideoURL,
			Title:       episode.Title,
			Description: episode.Description,
			// Some app prefer 1-based order
			IOrder: strconv.Itoa(i + 1),
		}

		if episode.Link != "" {
			item.Link = episode.Link
		}

		item.AddPubDate(&episode.PubDate)
		summary := episode.Description
		if episode.Summary != "" {
			summary = episode.Summary
		}
		item.AddSummary(summary)
		item.AddImage(episode.Thumbnail)
		item.AddDuration(episode.Duration)
		item.IDuration = formatDurationHHMMSS(episode.Duration)
		if episode.Author != "" {
			item.IAuthor = episode.Author
			item.AuthorFormatted = episode.Author
		}

		if episode.MetadataSource == "rss" {
			logger.WithFields(log.Fields{
				"episode_id": episode.ID,
				"source":     "episode.Title",
				"target":     "item.Title",
				"value":      item.Title,
			}).Debug("[metadata] Wrote normalized metadata to XML item")
			logger.WithFields(log.Fields{
				"episode_id": episode.ID,
				"source":     "episode.Description",
				"target":     "item.Description",
				"value":      item.Description,
			}).Debug("[metadata] Wrote normalized metadata to XML item")
			logger.WithFields(log.Fields{
				"episode_id": episode.ID,
				"source":     "episode.Summary",
				"target":     "item.ISummary",
				"value":      summary,
			}).Debug("[metadata] Wrote normalized metadata to XML item")

			if episode.Keywords != "" {
				logger.WithFields(log.Fields{
					"episode_id": episode.ID,
					"source":     "episode.Keywords",
					"reason":     "github.com/eduncan911/podcast does not expose itunes:keywords support",
				}).Debug("[metadata] Skipped source field during XML write")
			}
			if episode.Season > 0 {
				logger.WithFields(log.Fields{
					"episode_id": episode.ID,
					"source":     "episode.Season",
					"reason":     "github.com/eduncan911/podcast does not expose itunes:season support",
				}).Debug("[metadata] Skipped source field during XML write")
			}
			if episode.EpisodeNumber > 0 {
				logger.WithFields(log.Fields{
					"episode_id": episode.ID,
					"source":     "episode.EpisodeNumber",
					"reason":     "github.com/eduncan911/podcast does not expose itunes:episode support",
				}).Debug("[metadata] Skipped source field during XML write")
			}
			if episode.EpisodeType != "" {
				logger.WithFields(log.Fields{
					"episode_id": episode.ID,
					"source":     "episode.EpisodeType",
					"reason":     "github.com/eduncan911/podcast does not expose itunes:episodeType support",
				}).Debug("[metadata] Skipped source field during XML write")
			}
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

func RenderXML(podcast *itunes.Podcast, episodes []*model.Episode) (string, error) {
	if podcast == nil {
		return "", errors.New("podcast is nil")
	}

	doc := buildRSSDocument(podcast, episodes)
	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal rss")
	}
	return xml.Header + string(data), nil
}

type rssDocument struct {
	XMLName      xml.Name   `xml:"rss"`
	Version      string     `xml:"version,attr"`
	XmlnsAtom    string     `xml:"xmlns:atom,attr,omitempty"`
	XmlnsContent string     `xml:"xmlns:content,attr,omitempty"`
	XmlnsItunes  string     `xml:"xmlns:itunes,attr,omitempty"`
	Channel      rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string              `xml:"title"`
	Link          string              `xml:"link"`
	Description   string              `xml:"description"`
	Category      string              `xml:"category,omitempty"`
	Cloud         string              `xml:"cloud,omitempty"`
	Copyright     string              `xml:"copyright,omitempty"`
	Docs          string              `xml:"docs,omitempty"`
	Generator     string              `xml:"generator,omitempty"`
	Language      string              `xml:"language,omitempty"`
	LastBuildDate string              `xml:"lastBuildDate,omitempty"`
	PubDate       string              `xml:"pubDate,omitempty"`
	Rating        string              `xml:"rating,omitempty"`
	SkipHours     string              `xml:"skipHours,omitempty"`
	SkipDays      string              `xml:"skipDays,omitempty"`
	TTL           int                 `xml:"ttl,omitempty"`
	WebMaster     string              `xml:"webMaster,omitempty"`
	Image         *itunes.Image       `xml:"image,omitempty"`
	TextInput     *itunes.TextInput   `xml:"textInput,omitempty"`
	AtomLink      *itunes.AtomLink    `xml:"atom:link,omitempty"`
	IAuthor       string              `xml:"itunes:author,omitempty"`
	ISummary      *itunes.ISummary    `xml:"itunes:summary,omitempty"`
	IBlock        string              `xml:"itunes:block,omitempty"`
	IImage        *itunes.IImage      `xml:"itunes:image,omitempty"`
	IDuration     string              `xml:"itunes:duration,omitempty"`
	IExplicit     string              `xml:"itunes:explicit,omitempty"`
	IComplete     string              `xml:"itunes:complete,omitempty"`
	INewFeedURL   string              `xml:"itunes:new-feed-url,omitempty"`
	IOwner        *itunes.Author      `xml:"itunes:owner,omitempty"`
	ICategories   []*itunes.ICategory `xml:"itunes:category,omitempty"`
	Items         []rssItem           `xml:"item"`
}

type rssItem struct {
	GUID               string            `xml:"guid"`
	Title              *cdataNode        `xml:"title,omitempty"`
	Link               string            `xml:"link,omitempty"`
	Description        *cdataNode        `xml:"description,omitempty"`
	AuthorFormatted    string            `xml:"author,omitempty"`
	Category           string            `xml:"category,omitempty"`
	Comments           string            `xml:"comments,omitempty"`
	Source             string            `xml:"source,omitempty"`
	PubDateFormatted   string            `xml:"pubDate,omitempty"`
	Enclosure          *itunes.Enclosure `xml:"enclosure,omitempty"`
	IAuthor            string            `xml:"itunes:author,omitempty"`
	ISummary           *itunes.ISummary  `xml:"itunes:summary,omitempty"`
	IImage             *itunes.IImage    `xml:"itunes:image,omitempty"`
	IDuration          string            `xml:"itunes:duration,omitempty"`
	IExplicit          string            `xml:"itunes:explicit,omitempty"`
	IIsClosedCaptioned string            `xml:"itunes:isClosedCaptioned,omitempty"`
	IOrder             string            `xml:"itunes:order,omitempty"`
	ISeason            int               `xml:"itunes:season,omitempty"`
	IEpisode           int               `xml:"itunes:episode,omitempty"`
	IEpisodeType       string            `xml:"itunes:episodeType,omitempty"`
}

type cdataNode struct {
	Value string
}

func (c *cdataNode) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if c == nil {
		return nil
	}
	type cdataElement struct {
		Inner string `xml:",innerxml"`
	}
	inner := "<![CDATA[" + strings.ReplaceAll(c.Value, "]]>", "]]]]><![CDATA[>") + "]]>"
	return e.EncodeElement(cdataElement{Inner: inner}, start)
}

func buildRSSDocument(podcast *itunes.Podcast, episodes []*model.Episode) rssDocument {
	doc := rssDocument{
		Version:      "2.0",
		XmlnsAtom:    "http://www.w3.org/2005/Atom",
		XmlnsContent: "http://purl.org/rss/1.0/modules/content/",
		XmlnsItunes:  "http://www.itunes.com/dtds/podcast-1.0.dtd",
		Channel: rssChannel{
			Title:         podcast.Title,
			Link:          podcast.Link,
			Description:   podcast.Description,
			Category:      podcast.Category,
			Cloud:         podcast.Cloud,
			Copyright:     podcast.Copyright,
			Docs:          podcast.Docs,
			Generator:     podcast.Generator,
			Language:      podcast.Language,
			LastBuildDate: podcast.LastBuildDate,
			PubDate:       podcast.PubDate,
			Rating:        podcast.Rating,
			SkipHours:     podcast.SkipHours,
			SkipDays:      podcast.SkipDays,
			TTL:           podcast.TTL,
			WebMaster:     podcast.WebMaster,
			Image:         podcast.Image,
			TextInput:     podcast.TextInput,
			AtomLink:      podcast.AtomLink,
			IAuthor:       podcast.IAuthor,
			ISummary:      podcast.ISummary,
			IBlock:        podcast.IBlock,
			IImage:        podcast.IImage,
			IDuration:     podcast.IDuration,
			IExplicit:     podcast.IExplicit,
			IComplete:     podcast.IComplete,
			INewFeedURL:   podcast.INewFeedURL,
			IOwner:        podcast.IOwner,
			ICategories:   podcast.ICategories,
		},
	}

	for _, item := range podcast.Items {
		episode := findEpisodeByID(episodes, item.GUID)
		rssItem := rssItem{
			GUID:               item.GUID,
			Title:              newCDATA(item.Title),
			Link:               item.Link,
			Description:        newCDATA(item.Description),
			AuthorFormatted:    item.AuthorFormatted,
			Category:           item.Category,
			Comments:           item.Comments,
			Source:             item.Source,
			PubDateFormatted:   item.PubDateFormatted,
			Enclosure:          item.Enclosure,
			IAuthor:            item.IAuthor,
			ISummary:           item.ISummary,
			IImage:             item.IImage,
			IDuration:          item.IDuration,
			IExplicit:          item.IExplicit,
			IIsClosedCaptioned: item.IIsClosedCaptioned,
			IOrder:             item.IOrder,
		}
		if episode != nil {
			rssItem.ISeason = episode.Season
			rssItem.IEpisode = episode.EpisodeNumber
			rssItem.IEpisodeType = strings.TrimSpace(episode.EpisodeType)
		}
		doc.Channel.Items = append(doc.Channel.Items, rssItem)
	}

	return doc
}

func newCDATA(value string) *cdataNode {
	if value == "" {
		return nil
	}
	return &cdataNode{Value: value}
}

func findEpisodeByID(episodes []*model.Episode, id string) *model.Episode {
	for _, episode := range episodes {
		if episode != nil && episode.ID == id {
			return episode
		}
	}
	return nil
}

func formatDurationHHMMSS(total int64) string {
	if total <= 0 {
		return ""
	}
	hours := total / 3600
	minutes := (total % 3600) / 60
	seconds := total % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
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
