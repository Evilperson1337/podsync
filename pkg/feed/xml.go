package feed

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
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
		if episode.Status != model.EpisodeDownloaded {
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

		if episode.Subtitle != "" {
			item.ISubtitle = episode.Subtitle
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
				"source":     "episode.Subtitle",
				"target":     "item.ISubtitle",
				"value":      item.ISubtitle,
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

	xmlText := podcast.String()
	logger := log.WithField("component", "xml_render")

	for _, episode := range episodes {
		if episode == nil || episode.Status != model.EpisodeDownloaded {
			continue
		}

		var textUpdated bool
		xmlText, textUpdated = rewriteItemTextFields(xmlText, episode)
		if !textUpdated {
			logger.WithField("episode_id", episode.ID).Warn("[metadata] Failed to rewrite normalized item text fields in XML item")
		}

		extra := buildExtendedItemMetadataXML(episode, "      ")
		if extra == "" {
			continue
		}

		updated, injected := injectExtendedMetadataIntoItem(xmlText, episode.ID, extra)
		if !injected {
			logger.WithField("episode_id", episode.ID).Warn("[metadata] Failed to inject extended RSS metadata into XML item")
			continue
		}

		xmlText = updated
		logger.WithFields(log.Fields{
			"episode_id":     episode.ID,
			"itunes_season":  episode.Season,
			"itunes_episode": episode.EpisodeNumber,
			"episode_type":   episode.EpisodeType,
		}).Info("[metadata] Injected extended RSS metadata into XML item")
	}

	return xmlText, nil
}

func buildExtendedItemMetadataXML(episode *model.Episode, childIndent string) string {
	if episode == nil {
		return ""
	}
	if childIndent == "" {
		childIndent = "      "
	}

	var b strings.Builder
	if episode.Season > 0 {
		b.WriteString("\n")
		b.WriteString(childIndent)
		b.WriteString("<itunes:season>")
		b.WriteString(strconv.Itoa(episode.Season))
		b.WriteString("</itunes:season>")
	}
	if episode.EpisodeNumber > 0 {
		b.WriteString("\n")
		b.WriteString(childIndent)
		b.WriteString("<itunes:episode>")
		b.WriteString(strconv.Itoa(episode.EpisodeNumber))
		b.WriteString("</itunes:episode>")
	}
	if strings.TrimSpace(episode.EpisodeType) != "" {
		b.WriteString("\n")
		b.WriteString(childIndent)
		b.WriteString("<itunes:episodeType>")
		b.WriteString(escapeXMLText(strings.TrimSpace(episode.EpisodeType)))
		b.WriteString("</itunes:episodeType>")
	}
	return b.String()
}

func injectExtendedMetadataIntoItem(xmlText, guid, extra string) (string, bool) {
	if xmlText == "" || guid == "" || extra == "" {
		return xmlText, false
	}

	guidTag := "<guid>" + escapeXMLText(guid) + "</guid>"
	searchFrom := 0
	for {
		itemStartRel := strings.Index(xmlText[searchFrom:], "<item>")
		if itemStartRel < 0 {
			return xmlText, false
		}
		itemStart := searchFrom + itemStartRel
		itemEndRel := strings.Index(xmlText[itemStart:], "</item>")
		if itemEndRel < 0 {
			return xmlText, false
		}
		itemEnd := itemStart + itemEndRel
		itemXML := xmlText[itemStart:itemEnd]
		if strings.Contains(itemXML, guidTag) {
			trimmedEnd := itemEnd
			for trimmedEnd > itemStart {
				switch xmlText[trimmedEnd-1] {
				case ' ', '\t', '\n', '\r':
					trimmedEnd--
				default:
					goto trimmed
				}
			}
		trimmed:
			closingIndent := detectIndentBeforeIndex(xmlText, itemStart)
			formattedExtra := strings.TrimRight(extra, "\r\n")
			if closingIndent != "" {
				formattedExtra += "\n" + closingIndent
			}
			return xmlText[:trimmedEnd] + formattedExtra + xmlText[itemEnd:], true
		}
		searchFrom = itemEnd + len("</item>")
	}
}

func rewriteItemTextFields(xmlText string, episode *model.Episode) (string, bool) {
	if xmlText == "" || episode == nil || episode.ID == "" {
		return xmlText, false
	}

	itemXML, itemStart, itemEnd, ok := findItemXMLByGUID(xmlText, episode.ID)
	if !ok {
		return xmlText, false
	}

	updated := itemXML
	rewrote := false
	if episode.Title != "" {
		updated, rewrote = replaceFirstTagValueWithCDATA(updated, "title", episode.Title)
	}
	if episode.Description != "" {
		var replaced bool
		updated, replaced = replaceFirstTagValueWithCDATA(updated, "description", episode.Description)
		rewrote = rewrote || replaced
	}
	if episode.Subtitle != "" {
		var replaced bool
		updated, replaced = replaceFirstTagValueWithCDATA(updated, "itunes:subtitle", episode.Subtitle)
		rewrote = rewrote || replaced
	}

	if !rewrote {
		return xmlText, false
	}

	return xmlText[:itemStart] + updated + xmlText[itemEnd:], true
}

func findItemXMLByGUID(xmlText, guid string) (string, int, int, bool) {
	guidTag := "<guid>" + escapeXMLText(guid) + "</guid>"
	searchFrom := 0
	for {
		itemStartRel := strings.Index(xmlText[searchFrom:], "<item>")
		if itemStartRel < 0 {
			return "", 0, 0, false
		}
		itemStart := searchFrom + itemStartRel
		itemEndRel := strings.Index(xmlText[itemStart:], "</item>")
		if itemEndRel < 0 {
			return "", 0, 0, false
		}
		itemEnd := itemStart + itemEndRel + len("</item>")
		itemXML := xmlText[itemStart:itemEnd]
		if strings.Contains(itemXML, guidTag) {
			return itemXML, itemStart, itemEnd, true
		}
		searchFrom = itemEnd
	}
}

func replaceFirstTagValueWithCDATA(itemXML, tagName, value string) (string, bool) {
	openTag := "<" + tagName + ">"
	closeTag := "</" + tagName + ">"
	start := strings.Index(itemXML, openTag)
	if start < 0 {
		return itemXML, false
	}
	valueStart := start + len(openTag)
	endRel := strings.Index(itemXML[valueStart:], closeTag)
	if endRel < 0 {
		return itemXML, false
	}
	valueEnd := valueStart + endRel
	replacement := openTag + wrapCDATA(value) + closeTag
	return itemXML[:start] + replacement + itemXML[valueEnd+len(closeTag):], true
}

func wrapCDATA(value string) string {
	return "<![CDATA[" + strings.ReplaceAll(value, "]]>", "]]]]><![CDATA[>") + "]]>"
}

var trailingIndentRegexp = regexp.MustCompile(`\n([ \t]*)$`)

func detectIndentBeforeIndex(value string, index int) string {
	if index <= 0 || index > len(value) {
		return ""
	}
	match := trailingIndentRegexp.FindStringSubmatch(value[:index])
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func escapeXMLText(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
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
