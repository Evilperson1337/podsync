package overlay

import (
	"context"
	"encoding/xml"
	stdhtml "html"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	xhtml "golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

const (
	rssMetadataSource     = "rss"
	rssOrderSource        = "rss_metadata"
	rssFuzzyThreshold     = 0.94
	rssDateSimilarityMin  = 0.75
	rssDateMatchThreshold = 24 * time.Hour
)

type RSSProvider struct {
	client *http.Client
}

type rssItem struct {
	GUID            string
	Title           string
	ItunesTitle     string
	Link            string
	Description     string
	ContentEncoded  string
	Keywords        string
	PubDate         time.Time
	Duration        int64
	Explicit        *bool
	Season          int
	Episode         int
	EpisodeType     string
	EnclosureURL    string
	EnclosureLength int64
	Author          string
	Thumbnail       string
	Order           int

	normalizedTitle string
	canonicalTitle  string
	canonicalDesc   string
}

type rssMatch struct {
	item       *rssItem
	strategy   string
	similarity float64
	dateDiff   time.Duration
}

func NewRSSProvider(client *http.Client) *RSSProvider {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &RSSProvider{client: client}
}

func (p *RSSProvider) Name() string {
	return "rss"
}

func (p *RSSProvider) Apply(ctx context.Context, cfg *feed.Config, result *model.Feed) error {
	rssURL := strings.TrimSpace(cfg.Custom.RSSMetadataURL)
	if rssURL == "" {
		log.WithField("feed_id", cfg.ID).Info("RSS metadata URL not configured; skipping RSS metadata retrieval")
		return nil
	}
	if result == nil {
		return nil
	}

	logger := log.WithFields(log.Fields{
		"feed_id": cfg.ID,
		"url":     rssURL,
		"overlay": "rss",
	})

	items, err := p.fetch(ctx, rssURL)
	if err != nil {
		logger.WithError(err).Error("Failed to obtain RSS Feed Metadata from configured URL")
		return nil
	}
	logger.WithField("items", len(items)).Info("Successfully obtained RSS Feed Metadata from configured URL")

	if len(result.Episodes) == 0 {
		logger.Info("No episodes available for RSS metadata matching in current feed job")
		return nil
	}

	matches, matchedCount := matchEpisodes(result.Episodes, items, logger)
	if matchedCount == 0 {
		logger.WithField("episodes", len(result.Episodes)).Info("No RSS metadata matches found for current feed job")
		return nil
	}

	applyRSSOrdering(result.Episodes, matches, logger)
	refreshFeedPubDate(result)
	return nil
}

func (p *RSSProvider) fetch(ctx context.Context, rssURL string) ([]*rssItem, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rssURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "PodsyncMetadataOverlay/1.0 (+https://github.com/mxpv/podsync)")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{statusCode: resp.StatusCode}
	}

	return parseRSSFeed(resp.Body, log.WithFields(log.Fields{
		"overlay": "rss",
		"url":     rssURL,
	}))
}

type httpStatusError struct {
	statusCode int
}

func (e *httpStatusError) Error() string {
	return "unexpected rss status: " + strconv.Itoa(e.statusCode)
}

func parseRSSFeed(reader io.Reader, logger log.FieldLogger) ([]*rssItem, error) {
	decoder := xml.NewDecoder(reader)
	items := make([]*rssItem, 0)
	order := 1

	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "item" {
			continue
		}

		item, err := parseRSSItem(decoder, start, order, logger.WithField("rss_order", order))
		if err != nil {
			return nil, err
		}
		order++
		if item == nil {
			continue
		}

		item.canonicalTitle = chooseCanonicalTitle(item)
		item.canonicalDesc = chooseCanonicalDescription(item)
		item.normalizedTitle = normalizeTitle(item.canonicalTitle)
		items = append(items, item)
	}

	return items, nil
}

func parseRSSItem(decoder *xml.Decoder, start xml.StartElement, order int, logger log.FieldLogger) (*rssItem, error) {
	item := &rssItem{Order: order}

	for {
		tok, err := decoder.Token()
		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "guid":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.GUID = strings.TrimSpace(value)
			case "title":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				if t.Name.Space == "" {
					item.Title = normalizeAndLogRSSTextField("title", "rssItem.Title", value, logger)
				} else {
					item.ItunesTitle = normalizeAndLogRSSTextField("itunes:title", "rssItem.ItunesTitle", value, logger)
				}
			case "link":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.Link = normalizeAndLogRSSTextField("link", "rssItem.Link", value, logger)
			case "description":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.Description = normalizeAndLogRSSTextField("description", "rssItem.Description", value, logger)
			case "encoded":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.ContentEncoded = normalizeAndLogRSSTextField("content:encoded", "rssItem.ContentEncoded", value, logger)
			case "pubDate":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.PubDate = parseRSSPubDate(value)
			case "duration":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.Duration = parseRSSDuration(value)
			case "explicit":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.Explicit = parseExplicit(value)
			case "season":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.Season, _ = strconv.Atoi(strings.TrimSpace(value))
			case "episode":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.Episode, _ = strconv.Atoi(strings.TrimSpace(value))
			case "episodeType":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.EpisodeType = normalizeAndLogRSSTextField("itunes:episodeType", "rssItem.EpisodeType", value, logger)
			case "author":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.Author = normalizeAndLogRSSTextField("itunes:author", "rssItem.Author", value, logger)
			case "keywords":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				item.Keywords = normalizeAndLogRSSTextField("itunes:keywords", "rssItem.Keywords", value, logger)
			case "image":
				for _, attr := range t.Attr {
					if attr.Name.Local == "href" {
						item.Thumbnail = attr.Value
						break
					}
				}
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
			case "enclosure":
				for _, attr := range t.Attr {
					switch attr.Name.Local {
					case "url":
						item.EnclosureURL = attr.Value
					case "length":
						item.EnclosureLength, _ = strconv.ParseInt(attr.Value, 10, 64)
					}
				}
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
			default:
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return item, nil
			}
		}
	}
}

func parseRSSPubDate(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}

	layouts := []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC()
		}
	}

	return time.Time{}
}

func parseRSSDuration(value string) int64 {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) == 0 {
		return 0
	}

	var total int64
	multiplier := int64(1)
	for i := len(parts) - 1; i >= 0; i-- {
		part, err := strconv.ParseInt(strings.TrimSpace(parts[i]), 10, 64)
		if err != nil {
			return 0
		}
		total += part * multiplier
		multiplier *= 60
	}

	return total
}

func parseExplicit(value string) *bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes", "true", "explicit":
		result := true
		return &result
	case "no", "false", "clean":
		result := false
		return &result
	default:
		return nil
	}
}

func chooseCanonicalTitle(item *rssItem) string {
	if strings.TrimSpace(item.ItunesTitle) != "" {
		return strings.TrimSpace(item.ItunesTitle)
	}
	return strings.TrimSpace(item.Title)
}

func chooseCanonicalDescription(item *rssItem) string {
	if strings.TrimSpace(item.ContentEncoded) != "" {
		return strings.TrimSpace(item.ContentEncoded)
	}
	if extracted := extractPrimaryDescriptionText(item.Description, log.WithField("rss_guid", item.GUID)); extracted != "" {
		return extracted
	}
	return strings.TrimSpace(item.Description)
}

func chooseRSSItemTitle(item *rssItem) string {
	if strings.TrimSpace(item.Title) != "" {
		return strings.TrimSpace(item.Title)
	}
	return strings.TrimSpace(item.ItunesTitle)
}

func chooseRSSItemSubtitle(item *rssItem) string {
	if strings.TrimSpace(item.ItunesTitle) != "" {
		return strings.TrimSpace(item.ItunesTitle)
	}
	return strings.TrimSpace(item.Title)
}

func chooseRSSItemSummary(item *rssItem) string {
	if strings.TrimSpace(item.ContentEncoded) != "" {
		return strings.TrimSpace(item.ContentEncoded)
	}
	return strings.TrimSpace(item.Description)
}

var whitespaceRegexp = regexp.MustCompile(`\s+`)

func normalizeTitle(value string) string {
	value = stdhtml.UnescapeString(strings.TrimSpace(value))
	if value == "" {
		return ""
	}

	replacer := strings.NewReplacer(
		"’", "'",
		"‘", "'",
		"`", "'",
		"“", `"`,
		"”", `"`,
		"–", " ",
		"—", " ",
		"…", " ",
	)
	value = replacer.Replace(value)
	value = norm.NFKD.String(value)
	value = strings.ToLower(value)

	var b strings.Builder
	b.Grow(len(value))
	lastSpace := true
	for _, r := range value {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}

	return strings.TrimSpace(whitespaceRegexp.ReplaceAllString(b.String(), " "))
}

func matchEpisodes(episodes []*model.Episode, items []*rssItem, logger log.FieldLogger) (map[string]*rssMatch, int) {
	byTitle := make(map[string][]*rssItem)
	for _, item := range items {
		if item == nil || item.normalizedTitle == "" {
			continue
		}
		byTitle[item.normalizedTitle] = append(byTitle[item.normalizedTitle], item)
	}

	used := make(map[*rssItem]struct{})
	result := make(map[string]*rssMatch, len(episodes))
	matched := 0

	for _, episode := range episodes {
		if episode == nil {
			continue
		}

		match := findRSSMatch(episode, items, byTitle, used)
		if match == nil {
			logger.WithFields(log.Fields{
				"episode_id": episode.ID,
				"title":      episode.Title,
			}).Info("No RSS metadata match found for video")
			continue
		}

		used[match.item] = struct{}{}
		result[episode.ID] = match
		matched++

		logger.WithFields(log.Fields{
			"episode_id": episode.ID,
			"rss_guid":   match.item.GUID,
			"strategy":   match.strategy,
			"similarity": strconv.FormatFloat(match.similarity, 'f', 3, 64),
		}).Info("RSS Metadata match found for video")

		applyRSSMetadata(episode, match.item, logger.WithFields(log.Fields{
			"episode_id": episode.ID,
			"rss_guid":   match.item.GUID,
		}))
	}

	return result, matched
}

func findRSSMatch(episode *model.Episode, items []*rssItem, byTitle map[string][]*rssItem, used map[*rssItem]struct{}) *rssMatch {
	normalized := normalizeTitle(episode.Title)
	if normalized != "" {
		if exact := chooseExactMatch(episode, byTitle[normalized], used); exact != nil {
			return &rssMatch{item: exact, strategy: "exact_title", similarity: 1}
		}
	}

	if fuzzy := chooseFuzzyMatch(episode, items, used); fuzzy != nil {
		return fuzzy
	}

	if date := chooseDateMatch(episode, items, used); date != nil {
		return date
	}

	return nil
}

func chooseExactMatch(episode *model.Episode, candidates []*rssItem, used map[*rssItem]struct{}) *rssItem {
	var best *rssItem
	bestDiff := time.Duration(math.MaxInt64)
	for _, item := range candidates {
		if _, ok := used[item]; ok {
			continue
		}
		diff := absDuration(episode.PubDate.Sub(item.PubDate))
		if best == nil || diff < bestDiff || (diff == bestDiff && item.Order < best.Order) {
			best = item
			bestDiff = diff
		}
	}
	return best
}

func chooseFuzzyMatch(episode *model.Episode, items []*rssItem, used map[*rssItem]struct{}) *rssMatch {
	normalized := normalizeTitle(episode.Title)
	if normalized == "" {
		return nil
	}

	var best *rssItem
	bestScore := 0.0
	for _, item := range items {
		if _, ok := used[item]; ok || item.normalizedTitle == "" {
			continue
		}
		score := jaroWinkler(normalized, item.normalizedTitle)
		if score > bestScore {
			best = item
			bestScore = score
		}
	}

	if best == nil || bestScore < rssFuzzyThreshold {
		return nil
	}

	return &rssMatch{
		item:       best,
		strategy:   "fuzzy_title",
		similarity: bestScore,
		dateDiff:   absDuration(episode.PubDate.Sub(best.PubDate)),
	}
}

func chooseDateMatch(episode *model.Episode, items []*rssItem, used map[*rssItem]struct{}) *rssMatch {
	normalized := normalizeTitle(episode.Title)
	var best *rssItem
	bestDiff := rssDateMatchThreshold + time.Second
	bestScore := 0.0

	for _, item := range items {
		if _, ok := used[item]; ok || item.PubDate.IsZero() {
			continue
		}
		diff := absDuration(episode.PubDate.Sub(item.PubDate))
		if diff > rssDateMatchThreshold {
			continue
		}

		score := 0.0
		if normalized != "" && item.normalizedTitle != "" {
			score = jaroWinkler(normalized, item.normalizedTitle)
		}
		if score < rssDateSimilarityMin {
			continue
		}

		if best == nil || diff < bestDiff || (diff == bestDiff && score > bestScore) {
			best = item
			bestDiff = diff
			bestScore = score
		}
	}

	if best == nil {
		return nil
	}

	return &rssMatch{item: best, strategy: "publish_date", similarity: bestScore, dateDiff: bestDiff}
}

func applyRSSMetadata(episode *model.Episode, item *rssItem, logger log.FieldLogger) {
	assignedTitle := episode.Title
	if title := chooseRSSItemTitle(item); title != "" {
		episode.Title = title
		assignedTitle = title
		logger.WithField("field", "title").Debug("[metadata] Assigned source field title -> episode.Title")
	}
	if subtitle := chooseRSSItemSubtitle(item); subtitle != "" {
		episode.Subtitle = subtitle
		logger.WithField("field", "itunes:title").Debug("[metadata] Assigned source field itunes:title -> episode.Subtitle")
	}
	assignedDescription := episode.Description
	descriptionText := chooseRSSItemDescription(item, logger)
	if descriptionText != "" {
		episode.Description = descriptionText
		assignedDescription = descriptionText
		logger.WithField("field", "description").Debug("[metadata] Assigned source field description -> episode.Description")
	}
	assignedSummary := episode.Summary
	if descriptionText != "" {
		episode.Summary = descriptionText
		assignedSummary = descriptionText
		logger.WithField("field", "description").Debug("[metadata] Assigned normalized description text -> episode.Summary")
	} else if summary := chooseRSSItemSummary(item); summary != "" {
		normalizedSummary := normalizePlainText(summary)
		episode.Summary = normalizedSummary
		assignedSummary = normalizedSummary
		logger.WithField("field", "content:encoded").Debug("[metadata] Assigned fallback source field content:encoded -> episode.Summary")
	}
	if item.PubDate != (time.Time{}) {
		episode.PubDate = item.PubDate
		logger.WithField("field", "pubDate").Debug("[metadata] Assigned source field pubDate -> episode.PubDate")
	}
	if strings.TrimSpace(item.Link) != "" {
		episode.Link = strings.TrimSpace(item.Link)
		logger.WithField("field", "link").Debug("[metadata] Assigned source field link -> episode.Link")
	}
	if strings.TrimSpace(item.Author) != "" {
		episode.Author = strings.TrimSpace(item.Author)
		logger.WithField("field", "itunes:author").Debug("[metadata] Assigned source field itunes:author -> episode.Author")
	}
	if strings.TrimSpace(item.Keywords) != "" {
		episode.Keywords = strings.TrimSpace(item.Keywords)
		logger.WithField("field", "itunes:keywords").Debug("[metadata] Assigned source field itunes:keywords -> episode.Keywords")
	}
	if item.Season > 0 {
		episode.Season = item.Season
		logger.WithField("field", "itunes:season").Debug("[metadata] Assigned source field itunes:season -> episode.Season")
	}
	if item.Episode > 0 {
		episode.EpisodeNumber = item.Episode
		logger.WithField("field", "itunes:episode").Debug("[metadata] Assigned source field itunes:episode -> episode.EpisodeNumber")
	}
	if strings.TrimSpace(item.EpisodeType) != "" {
		episode.EpisodeType = strings.TrimSpace(item.EpisodeType)
		logger.WithField("field", "itunes:episodeType").Debug("[metadata] Assigned source field itunes:episodeType -> episode.EpisodeType")
	}
	if item.Explicit != nil {
		episode.Explicit = item.Explicit
		logger.WithField("field", "itunes:explicit").Debug("[metadata] Assigned source field itunes:explicit -> episode.Explicit")
	}
	if strings.TrimSpace(item.Thumbnail) != "" {
		episode.Thumbnail = strings.TrimSpace(item.Thumbnail)
		logger.WithField("field", "itunes:image").Debug("[metadata] Assigned source field itunes:image -> episode.Thumbnail")
	}
	episode.MetadataSource = rssMetadataSource

	logger.WithFields(log.Fields{
		"title":         assignedTitle,
		"pubDate":       episode.PubDate.Format(time.RFC1123Z),
		"duration":      formatDurationHHMMSS(episode.Duration),
		"enclosure_url": episode.VideoURL,
		"explicit":      formatExplicitValue(episode.Explicit),
		"season":        episode.Season,
		"episode":       episode.EpisodeNumber,
		"episode_type":  episode.EpisodeType,
	}).Info("Applied RSS metadata to episode")
	logger.WithFields(log.Fields{
		"description": assignedDescription,
		"summary":     assignedSummary,
	}).Debug("[metadata] Applied normalized text fields to episode")
}

func normalizeMetadataValue(field, value string, logger log.FieldLogger) string {
	raw := strings.TrimSpace(value)
	normalized := stdhtml.UnescapeString(raw)
	logger.WithFields(log.Fields{
		"field":      field,
		"raw":        raw,
		"normalized": normalized,
	}).Debug("[metadata] Normalized RSS metadata field")
	return normalized
}

func chooseRSSItemDescription(item *rssItem, logger log.FieldLogger) string {
	if desc := extractPrimaryDescriptionText(item.Description, logger); desc != "" {
		return desc
	}
	if strings.TrimSpace(item.Description) != "" {
		logger.WithField("source_field", "description").Debug("[metadata] Falling back to normalized RSS description text")
		return normalizePlainText(item.Description)
	}
	if summary := extractPrimaryDescriptionText(item.ContentEncoded, logger); summary != "" {
		logger.WithField("source_field", "content:encoded").Debug("[metadata] Falling back to extracted content:encoded paragraph for description")
		return summary
	}
	return normalizePlainText(item.ContentEncoded)
}

func extractPrimaryDescriptionText(raw string, logger log.FieldLogger) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	logger.WithFields(log.Fields{
		"source_field": "description",
		"raw":          raw,
	}).Debug("[metadata] Attempting to extract first RSS description paragraph")

	nodes, err := xhtml.ParseFragment(strings.NewReader(raw), nil)
	if err != nil {
		logger.WithError(err).WithField("source_field", "description").Debug("[metadata] Failed to parse RSS description HTML; falling back")
		return normalizePlainText(raw)
	}

	for _, node := range nodes {
		if paragraph := findPrimaryDescriptionParagraph(node); paragraph != nil {
			text := normalizePlainText(extractNodeText(paragraph))
			logger.WithFields(log.Fields{
				"source_field": "description",
				"extracted":    text,
			}).Debug("[metadata] Extracted first RSS description paragraph")
			return text
		}
	}

	for _, node := range nodes {
		if paragraph := findFirstParagraph(node); paragraph != nil {
			text := normalizePlainText(extractNodeText(paragraph))
			logger.WithFields(log.Fields{
				"source_field": "description",
				"extracted":    text,
			}).Debug("[metadata] Primary description paragraph missing; using first paragraph fallback")
			return text
		}
	}

	fallback := normalizePlainText(raw)
	logger.WithFields(log.Fields{
		"source_field": "description",
		"extracted":    fallback,
	}).Debug("[metadata] No paragraph tags found in RSS description; using normalized text fallback")
	return fallback
}

func findPrimaryDescriptionParagraph(node *xhtml.Node) *xhtml.Node {
	if node == nil {
		return nil
	}
	if node.Type == xhtml.ElementNode && node.Data == "p" && hasAllClasses(node, "media-description", "media-description--first") {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findPrimaryDescriptionParagraph(child); found != nil {
			return found
		}
	}
	return nil
}

func findFirstParagraph(node *xhtml.Node) *xhtml.Node {
	if node == nil {
		return nil
	}
	if node.Type == xhtml.ElementNode && node.Data == "p" {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := findFirstParagraph(child); found != nil {
			return found
		}
	}
	return nil
}

func hasAllClasses(node *xhtml.Node, required ...string) bool {
	if node == nil {
		return false
	}
	for _, attr := range node.Attr {
		if attr.Key != "class" {
			continue
		}
		classes := strings.Fields(attr.Val)
		set := make(map[string]struct{}, len(classes))
		for _, className := range classes {
			set[className] = struct{}{}
		}
		for _, need := range required {
			if _, ok := set[need]; !ok {
				return false
			}
		}
		return true
	}
	return false
}

func extractNodeText(node *xhtml.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == xhtml.TextNode {
		return node.Data
	}
	var parts []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if text := extractNodeText(child); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

func normalizePlainText(value string) string {
	value = stdhtml.UnescapeString(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	return strings.TrimSpace(whitespaceRegexp.ReplaceAllString(value, " "))
}

func formatDurationHHMMSS(total int64) string {
	if total <= 0 {
		return ""
	}
	hours := total / 3600
	minutes := (total % 3600) / 60
	seconds := total % 60
	return strconv.FormatInt(hours/10, 10) + strconv.FormatInt(hours%10, 10) + ":" +
		strconv.FormatInt(minutes/10, 10) + strconv.FormatInt(minutes%10, 10) + ":" +
		strconv.FormatInt(seconds/10, 10) + strconv.FormatInt(seconds%10, 10)
}

func formatExplicitValue(explicit *bool) interface{} {
	if explicit == nil {
		return ""
	}
	return *explicit
}

func normalizeAndLogRSSTextField(sourceField, targetField, value string, logger log.FieldLogger) string {
	raw := strings.TrimSpace(value)
	logger.WithFields(log.Fields{
		"source_field": sourceField,
		"raw":          raw,
	}).Debug("[metadata] Source field extracted")
	normalized := normalizeMetadataValue(sourceField, raw, logger)
	logger.WithFields(log.Fields{
		"source_field":   sourceField,
		"internal_field": targetField,
	}).Debug("[metadata] Assigned source field to internal metadata")
	return normalized
}

func applyRSSOrdering(episodes []*model.Episode, matches map[string]*rssMatch, logger log.FieldLogger) {
	matched := make([]*model.Episode, 0, len(matches))
	unmatched := make([]*model.Episode, 0, len(episodes)-len(matches))
	conflict := false

	for idx, episode := range episodes {
		if episode == nil {
			continue
		}
		if match, ok := matches[episode.ID]; ok {
			matched = append(matched, episode)
			if current, ok := parseOrderValue(episode.Order); !ok || current != match.item.Order || current != idx+1 {
				conflict = true
			}
			continue
		}
		unmatched = append(unmatched, episode)
	}

	sort.SliceStable(matched, func(i, j int) bool {
		left := matches[matched[i].ID].item.Order
		right := matches[matched[j].ID].item.Order
		return left < right
	})
	sort.SliceStable(unmatched, func(i, j int) bool {
		if !unmatched[i].PubDate.Equal(unmatched[j].PubDate) {
			return unmatched[i].PubDate.After(unmatched[j].PubDate)
		}
		return unmatched[i].ID < unmatched[j].ID
	})

	ordered := append(matched, unmatched...)
	for idx, episode := range ordered {
		episode.Order = strconv.Itoa(idx + 1)
		episode.OrderSource = rssOrderSource
	}

	if conflict {
		logger.WithFields(log.Fields{
			"matched":   len(matched),
			"unmatched": len(unmatched),
		}).Info("ordering conflicts resolved using rss ordering for matched items; unmatched items appended by source publish date")
	}
}

func parseOrderValue(order string) (int, bool) {
	value, err := strconv.Atoi(order)
	if err != nil {
		return 0, false
	}
	return value, true
}

func refreshFeedPubDate(result *model.Feed) {
	if result == nil {
		return
	}
	for _, episode := range result.Episodes {
		if episode == nil || episode.PubDate.IsZero() {
			continue
		}
		if result.PubDate.IsZero() || episode.PubDate.After(result.PubDate) {
			result.PubDate = episode.PubDate
		}
	}
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func jaroWinkler(left, right string) float64 {
	if left == right {
		return 1
	}
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	leftLen := len(leftRunes)
	rightLen := len(rightRunes)
	if leftLen == 0 || rightLen == 0 {
		return 0
	}

	matchDistance := maxInt(leftLen, rightLen)/2 - 1
	if matchDistance < 0 {
		matchDistance = 0
	}

	leftMatches := make([]bool, leftLen)
	rightMatches := make([]bool, rightLen)
	matches := 0

	for i := 0; i < leftLen; i++ {
		start := maxInt(0, i-matchDistance)
		end := minInt(i+matchDistance+1, rightLen)
		for j := start; j < end; j++ {
			if rightMatches[j] || leftRunes[i] != rightRunes[j] {
				continue
			}
			leftMatches[i] = true
			rightMatches[j] = true
			matches++
			break
		}
	}

	if matches == 0 {
		return 0
	}

	transpositions := 0
	for i, j := 0, 0; i < leftLen; i++ {
		if !leftMatches[i] {
			continue
		}
		for ; j < rightLen; j++ {
			if rightMatches[j] {
				break
			}
		}
		if j < rightLen && leftRunes[i] != rightRunes[j] {
			transpositions++
		}
		j++
	}

	m := float64(matches)
	jaro := (m/float64(leftLen) + m/float64(rightLen) + (m-float64(transpositions)/2)/m) / 3

	prefix := 0
	for i := 0; i < minInt(4, minInt(leftLen, rightLen)); i++ {
		if leftRunes[i] != rightRunes[i] {
			break
		}
		prefix++
	}

	return jaro + float64(prefix)*0.1*(1-jaro)
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
