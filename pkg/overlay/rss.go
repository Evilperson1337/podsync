package overlay

import (
	"context"
	"encoding/xml"
	"html"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

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
	if rssURL == "" || result == nil || len(result.Episodes) == 0 {
		return nil
	}

	logger := log.WithFields(log.Fields{
		"feed_id": cfg.ID,
		"url":     rssURL,
		"overlay": "rss",
	})
	logger.Info("rss metadata overlay enabled")

	items, err := p.fetch(ctx, rssURL)
	if err != nil {
		logger.WithError(err).Warn("rss metadata feed fetch failure; falling back to source metadata")
		return nil
	}
	logger.WithField("items", len(items)).Info("rss metadata feed fetched successfully")

	matches, matchedCount := matchEpisodes(result.Episodes, items, logger)
	if matchedCount == 0 {
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

	return parseRSSFeed(resp.Body)
}

type httpStatusError struct {
	statusCode int
}

func (e *httpStatusError) Error() string {
	return "unexpected rss status: " + strconv.Itoa(e.statusCode)
}

func parseRSSFeed(reader io.Reader) ([]*rssItem, error) {
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

		item, err := parseRSSItem(decoder, start, order)
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

func parseRSSItem(decoder *xml.Decoder, start xml.StartElement, order int) (*rssItem, error) {
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
				if err := decoder.DecodeElement(&item.GUID, &t); err != nil {
					return nil, err
				}
			case "title":
				var value string
				if err := decoder.DecodeElement(&value, &t); err != nil {
					return nil, err
				}
				if t.Name.Space == "" {
					item.Title = value
				} else {
					item.ItunesTitle = value
				}
			case "link":
				if err := decoder.DecodeElement(&item.Link, &t); err != nil {
					return nil, err
				}
			case "description":
				if err := decoder.DecodeElement(&item.Description, &t); err != nil {
					return nil, err
				}
			case "encoded":
				if err := decoder.DecodeElement(&item.ContentEncoded, &t); err != nil {
					return nil, err
				}
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
				if err := decoder.DecodeElement(&item.EpisodeType, &t); err != nil {
					return nil, err
				}
			case "author":
				if err := decoder.DecodeElement(&item.Author, &t); err != nil {
					return nil, err
				}
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
	return strings.TrimSpace(item.Description)
}

var whitespaceRegexp = regexp.MustCompile(`\s+`)

func normalizeTitle(value string) string {
	value = html.UnescapeString(strings.TrimSpace(value))
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
			}).Warn("no rss metadata match found for source item")
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
		}).Info("rss metadata match found for source item")

		applyRSSMetadata(episode, match.item)
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

func applyRSSMetadata(episode *model.Episode, item *rssItem) {
	if title := strings.TrimSpace(item.canonicalTitle); title != "" {
		episode.Title = title
	}
	if desc := strings.TrimSpace(item.canonicalDesc); desc != "" {
		episode.Description = desc
	}
	if item.PubDate != (time.Time{}) {
		episode.PubDate = item.PubDate
	}
	if strings.TrimSpace(item.Link) != "" {
		episode.Link = strings.TrimSpace(item.Link)
	}
	if strings.TrimSpace(item.Author) != "" {
		episode.Author = strings.TrimSpace(item.Author)
	}
	if item.Explicit != nil {
		episode.Explicit = item.Explicit
	}
	if strings.TrimSpace(item.Thumbnail) != "" {
		episode.Thumbnail = strings.TrimSpace(item.Thumbnail)
	}
	episode.MetadataSource = rssMetadataSource
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
