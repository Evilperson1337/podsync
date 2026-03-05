package builder

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

const (
	rumbleBaseURL          = "https://rumble.com"
	rumbleUserAgent        = "PodsyncRumble/1.0 (+https://github.com/mxpv/podsync)"
	rumbleRateLimit        = 500 * time.Millisecond
	rumbleRequestJitterMin = 100 * time.Millisecond
	rumbleRequestJitterMax = 500 * time.Millisecond
	rumbleRetryAttempts    = 3
	rumbleCacheTTL         = 10 * time.Minute
)

type rumbleCacheEntry struct {
	items     []*model.Episode
	fetchedAt time.Time
}

type rumbleItem struct {
	episode *model.Episode
	status  string
}

type RumbleBuilder struct {
	client     *http.Client
	cache      map[string]rumbleCacheEntry
	cacheLock  sync.RWMutex
	clientSeed *rand.Rand
}

var (
	rumbleLimiterLock sync.Mutex
	rumbleLastRequest time.Time
)

func NewRumbleBuilder(_key string) (*RumbleBuilder, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	return &RumbleBuilder{
		client:     &http.Client{Timeout: 15 * time.Second, Transport: transport},
		cache:      make(map[string]rumbleCacheEntry),
		clientSeed: rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func (r *RumbleBuilder) Build(ctx context.Context, cfg *feed.Config) (*model.Feed, error) {
	info, err := ParseURL(cfg.URL)
	if err != nil {
		return nil, err
	}

	if info.Provider != model.ProviderRumble {
		return nil, errors.New("unsupported provider for rumble builder")
	}

	feedModel := &model.Feed{
		ItemID:          info.ItemID,
		Provider:        info.Provider,
		LinkType:        info.LinkType,
		Format:          cfg.Format,
		Quality:         cfg.Quality,
		CoverArtQuality: cfg.Custom.CoverArtQuality,
		PageSize:        cfg.PageSize,
		PlaylistSort:    cfg.PlaylistSort,
		PrivateFeed:     cfg.PrivateFeed,
		UpdatedAt:       time.Now().UTC(),
	}

	if feedModel.PageSize == 0 {
		feedModel.PageSize = model.DefaultPageSize
	}

	logger := log.WithFields(log.Fields{
		"provider":  "rumble",
		"channel":   info.ItemID,
		"link_type": info.LinkType,
		"page_size": feedModel.PageSize,
	})
	logger.Info("starting rumble scrape")

	feedURL, fallbackURL := rumbleChannelURL(info.ItemID, info.LinkType)
	known := r.cachedGUIDs(r.cacheKey(info.ItemID, info.LinkType))
	items, usedURL, err := r.fetchWithFallback(ctx, feedURL, fallbackURL, info.LinkType, feedModel.PageSize, known, logger)
	if err != nil {
		cacheKey := r.cacheKey(info.ItemID, info.LinkType)
		if cached, stale, ok := r.getCached(cacheKey); ok {
			if stale {
				logger.WithError(err).Warn("rumble scrape failed, serving stale cached results")
			} else {
				logger.WithError(err).Warn("rumble scrape failed, serving cached results")
			}
			if usedURL == "" {
				feedModel.ItemURL = feedURL
			} else {
				feedModel.ItemURL = usedURL
			}
			feedModel.Episodes = cached
			return feedModel, nil
		}
		return nil, err
	}

	items = r.dedupeWithOtherFeed(info.ItemID, info.LinkType, items, logger)
	feedModel.ItemURL = usedURL
	feedModel.Episodes = items
	feedModel.Title = fmt.Sprintf("Rumble %s", info.ItemID)
	feedModel.Author = info.ItemID
	if len(items) > 0 {
		feedModel.PubDate = items[0].PubDate
	}
	if cfg.Custom.Author != "" {
		feedModel.Author = cfg.Custom.Author
	}
	if cfg.Custom.Title != "" {
		feedModel.Title = cfg.Custom.Title
	}
	if cfg.Custom.Description != "" {
		feedModel.Description = cfg.Custom.Description
	}
	if cfg.Custom.CoverArt != "" {
		feedModel.CoverArt = cfg.Custom.CoverArt
	}

	if cfg.PlaylistSort == model.SortingAsc {
		sort.Slice(feedModel.Episodes, func(i, j int) bool {
			return feedModel.Episodes[i].PubDate.Before(feedModel.Episodes[j].PubDate)
		})
	}

	cacheKey := r.cacheKey(info.ItemID, info.LinkType)
	r.setCached(cacheKey, items)

	return feedModel, nil
}

func (r *RumbleBuilder) fetchWithFallback(
	ctx context.Context,
	primary string,
	fallback string,
	linkType model.Type,
	pageSize int,
	known map[string]struct{},
	logger log.FieldLogger,
) ([]*model.Episode, string, error) {
	items, err := r.scrapeListing(ctx, primary, linkType, pageSize, known, logger)
	if err == nil && len(items) > 0 {
		return items, primary, nil
	}
	logger.WithError(err).Warn("rumble primary listing failed, trying fallback")

	items, fallbackErr := r.scrapeListing(ctx, fallback, linkType, pageSize, known, logger)
	if fallbackErr != nil {
		if err != nil {
			return nil, fallback, errors.Wrapf(fallbackErr, "fallback failed after primary error: %v", err)
		}
		return nil, fallback, fallbackErr
	}
	return items, fallback, nil
}

func (r *RumbleBuilder) scrapeListing(ctx context.Context, startURL string, linkType model.Type, pageSize int, known map[string]struct{}, logger log.FieldLogger) ([]*model.Episode, error) {
	items := make([]*model.Episode, 0, pageSize)
	seen := make(map[string]struct{})
	visited := make(map[string]struct{})
	current := startURL
	page := 1

	for current != "" {
		if _, ok := visited[current]; ok {
			logger.WithField("url", current).Warn("rumble pagination loop detected")
			break
		}
		visited[current] = struct{}{}
		logger.WithFields(log.Fields{"url": current, "page": page}).Debug("fetching rumble page")

		html, err := r.fetchHTML(ctx, current)
		if err != nil {
			return nil, err
		}

		doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse rumble html")
		}

		pageItems, nextURL, parseErr := r.parseListing(doc, current, linkType, logger)
		if parseErr != nil {
			return nil, parseErr
		}

		for _, item := range pageItems {
			if item.episode == nil || item.episode.ID == "" {
				continue
			}
			logger.WithFields(log.Fields{
				"guid":     item.episode.ID,
				"title":    item.episode.Title,
				"duration": item.episode.Duration,
				"pub_date": item.episode.PubDate,
				"status":   item.status,
				"url":      item.episode.VideoURL,
			}).Debug("rumble parsed item")
			if _, ok := seen[item.episode.ID]; ok {
				continue
			}
			if _, ok := known[item.episode.ID]; ok {
				logger.WithField("guid", item.episode.ID).Debug("rumble known guid encountered, stopping pagination")
				return items, nil
			}
			seen[item.episode.ID] = struct{}{}
			items = append(items, item.episode)
			if len(items) >= pageSize {
				logger.WithField("count", len(items)).Debug("rumble page size reached")
				return items[:pageSize], nil
			}
		}

		if nextURL == "" {
			break
		}
		current = nextURL
		page++
	}

	return items, nil
}

func (r *RumbleBuilder) fetchHTML(ctx context.Context, pageURL string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= rumbleRetryAttempts; attempt++ {
		r.waitForRateLimit()
		r.sleepJitter()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return "", errors.Wrap(err, "failed to create rumble request")
		}
		req.Header.Set("User-Agent", rumbleUserAgent)
		req.Header.Set("Accept", "text/html")

		resp, err := r.client.Do(req)
		if err != nil {
			lastErr = errors.Wrap(err, "rumble request failed")
			r.sleepBackoff(attempt)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = errors.Wrap(err, "failed to read rumble response")
			r.sleepBackoff(attempt)
			continue
		}

		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
			lastErr = errors.Errorf("rumble status %d", resp.StatusCode)
			r.sleepBackoff(attempt)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", errors.Errorf("rumble status %d", resp.StatusCode)
		}

		if outputDir := strings.TrimSpace(os.Getenv("PODSYNC_RUMBLE_HTML_DIR")); outputDir != "" {
			timestamp := time.Now().UTC().Format("20060102-150405.000")
			filename := fmt.Sprintf("rumble-%s.html", timestamp)
			path := filepath.Join(outputDir, filename)
			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				return "", errors.Wrap(err, "failed to create rumble html output directory")
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				return "", errors.Wrap(err, "failed to write rumble html output")
			}
		}

		return string(body), nil
	}

	return "", errors.Wrap(lastErr, "rumble request failed after retries")
}

func (r *RumbleBuilder) parseListing(doc *goquery.Document, pageURL string, linkType model.Type, logger log.FieldLogger) ([]*rumbleItem, string, error) {
	items := make([]*rumbleItem, 0)
	found := false

	selectors := []string{
		".video-item",
		".video-item--grid",
		"article",
		"div.video-listing",
		".videostream, .thumbnail__container",
	}

	for _, selector := range selectors {
		selection := doc.Find(selector)
		if selection.Length() == 0 {
			continue
		}

		selection.Each(func(_ int, s *goquery.Selection) {
			item := parseRumbleItem(s, pageURL, logger)
			if item == nil || item.episode == nil {
				return
			}
			if linkType == model.TypeLivestreams {
				if item.status == "live" {
					logger.WithField("guid", item.episode.ID).Info("skipping live rumble stream")
					return
				}
				if item.status == "unknown" {
					logger.WithField("guid", item.episode.ID).Debug("rumble livestream status unknown")
				}
			}
			items = append(items, item)
			found = true
		})
		if found {
			break
		}
	}

	if !found {
		logger.WithField("url", pageURL).Warn("no rumble items found in listing")
	}

	return items, extractNextPageURL(doc, pageURL), nil
}

func parseRumbleItem(s *goquery.Selection, pageURL string, logger log.FieldLogger) *rumbleItem {
	urlSelectors := []string{"a[href*='/v']", "a.video-item--a", "a"}
	link := firstAttr(s, "href", urlSelectors)
	if link == "" {
		return nil
	}
	absURL := toAbsoluteURL(pageURL, link)
	guid := extractRumbleGUID(absURL)
	if guid == "" {
		return nil
	}

	titleSelectors := []string{".video-item--title", ".video-item-title", "h3", "h2", "a"}
	title := strings.TrimSpace(firstText(s, titleSelectors))

	thumbSelectors := []string{"img[src]", "img[data-src]", "img[data-original]"}
	thumbnail := firstAttr(s, "src", thumbSelectors)
	if thumbnail == "" {
		thumbnail = firstAttr(s, "data-src", thumbSelectors)
	}
	if thumbnail == "" {
		thumbnail = firstAttr(s, "data-original", thumbSelectors)
	}
	if thumbnail != "" {
		thumbnail = toAbsoluteURL(pageURL, thumbnail)
	}

	dateSelectors := []string{"time[datetime]", "time", ".video-item--meta time", ".video-item--time"}
	dateText := strings.TrimSpace(firstAttr(s, "datetime", dateSelectors))
	if dateText == "" {
		dateText = strings.TrimSpace(firstText(s, dateSelectors))
	}
	pubDate, dateStatus := parseRumbleDate(dateText)
	if pubDate.IsZero() {
		logger.WithField("guid", guid).Warn("rumble missing publish date, using scrape time")
		pubDate = time.Now().UTC()
	}

	durationSelectors := []string{
		".video-item--duration",
		".video-item-duration",
		".videostream__status--duration",
		".videostream__badge.videostream__status--duration",
		".duration",
	}
	durationText := strings.TrimSpace(firstText(s, durationSelectors))
	duration := parseDuration(durationText)

	statusText := strings.ToLower(strings.TrimSpace(s.Find(".video-item--live, .live-label, .badge--live, .live").First().Text()))
	status := "complete"
	if strings.Contains(statusText, "live") {
		status = "live"
	} else if dateStatus == "unknown" {
		status = "unknown"
	}

	return &rumbleItem{
		episode: &model.Episode{
			ID:          guid,
			Title:       title,
			Description: "",
			Thumbnail:   thumbnail,
			Duration:    duration,
			VideoURL:    absURL,
			PubDate:     pubDate,
			Status:      model.EpisodeNew,
		},
		status: status,
	}
}

func extractRumbleGUID(link string) string {
	regex := regexp.MustCompile(`/v([a-z0-9]+)`)
	matches := regex.FindStringSubmatch(link)
	if len(matches) < 2 {
		return ""
	}
	return "v" + matches[1]
}

func extractNextPageURL(doc *goquery.Document, base string) string {
	selectors := []string{
		"a[rel='next']",
		"a.next",
		"a:contains('Next')",
		"a.pagination-next",
	}
	for _, selector := range selectors {
		if link, exists := doc.Find(selector).First().Attr("href"); exists && link != "" {
			return toAbsoluteURL(base, link)
		}
	}

	// fallback: look for ?page= links
	link := ""
	doc.Find("a[href*='page=']").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		value, ok := s.Attr("href")
		if ok && value != "" {
			link = value
			return false
		}
		return true
	})
	if link != "" {
		return toAbsoluteURL(base, link)
	}

	return ""
}

func rumbleChannelURL(channel string, linkType model.Type) (string, string) {
	primary := fmt.Sprintf("%s/c/%s", rumbleBaseURL, channel)
	fallback := fmt.Sprintf("%s/user/%s", rumbleBaseURL, channel)
	if linkType == model.TypeLivestreams {
		primary += "/livestreams"
		fallback += "/livestreams"
	}
	return primary, fallback
}

func (r *RumbleBuilder) cacheKey(channel string, linkType model.Type) string {
	return fmt.Sprintf("%s:%s", channel, linkType)
}

func (r *RumbleBuilder) cachedGUIDs(key string) map[string]struct{} {
	result := map[string]struct{}{}
	r.cacheLock.RLock()
	defer r.cacheLock.RUnlock()
	entry, ok := r.cache[key]
	if !ok {
		return result
	}
	for _, item := range entry.items {
		if item != nil && item.ID != "" {
			result[item.ID] = struct{}{}
		}
	}
	return result
}

func (r *RumbleBuilder) dedupeWithOtherFeed(channel string, linkType model.Type, items []*model.Episode, logger log.FieldLogger) []*model.Episode {
	otherType := model.TypeChannel
	if linkType == model.TypeChannel {
		otherType = model.TypeLivestreams
	}
	otherKey := r.cacheKey(channel, otherType)
	otherGUIDs := r.cachedGUIDs(otherKey)
	if len(otherGUIDs) == 0 {
		return items
	}

	filtered := make([]*model.Episode, 0, len(items))
	for _, item := range items {
		if item == nil || item.ID == "" {
			continue
		}
		if _, ok := otherGUIDs[item.ID]; ok {
			logger.WithField("guid", item.ID).Debug("rumble cross-feed dedupe")
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func (r *RumbleBuilder) getCached(key string) ([]*model.Episode, bool, bool) {
	r.cacheLock.RLock()
	defer r.cacheLock.RUnlock()
	entry, ok := r.cache[key]
	if !ok {
		return nil, false, false
	}
	stale := time.Since(entry.fetchedAt) > rumbleCacheTTL
	return entry.items, stale, true
}

func (r *RumbleBuilder) setCached(key string, items []*model.Episode) {
	r.cacheLock.Lock()
	defer r.cacheLock.Unlock()
	r.cache[key] = rumbleCacheEntry{items: items, fetchedAt: time.Now().UTC()}
}

func (r *RumbleBuilder) waitForRateLimit() {
	rumbleLimiterLock.Lock()
	defer rumbleLimiterLock.Unlock()
	now := time.Now()
	if !rumbleLastRequest.IsZero() {
		wait := rumbleLastRequest.Add(rumbleRateLimit).Sub(now)
		if wait > 0 {
			time.Sleep(wait)
		}
	}
	rumbleLastRequest = time.Now()
}

func (r *RumbleBuilder) sleepJitter() {
	min := int64(rumbleRequestJitterMin)
	max := int64(rumbleRequestJitterMax)
	if max <= min {
		time.Sleep(rumbleRequestJitterMin)
		return
	}
	delta := r.clientSeed.Int63n(max-min) + min
	time.Sleep(time.Duration(delta))
}

func (r *RumbleBuilder) sleepBackoff(attempt int) {
	backoff := time.Duration(math.Pow(2, float64(attempt-1))) * 500 * time.Millisecond
	backoff += time.Duration(r.clientSeed.Int63n(int64(250 * time.Millisecond)))
	time.Sleep(backoff)
}

func parseDuration(text string) int64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	parts := strings.Split(text, ":")
	if len(parts) == 2 {
		minutes, _ := parseInt(parts[0])
		seconds, _ := parseInt(parts[1])
		return int64(minutes*60 + seconds)
	}
	if len(parts) == 3 {
		hours, _ := parseInt(parts[0])
		minutes, _ := parseInt(parts[1])
		seconds, _ := parseInt(parts[2])
		return int64(hours*3600 + minutes*60 + seconds)
	}
	return 0
}

func parseInt(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("empty number")
	}
	result := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("invalid number")
		}
		result = result*10 + int(r-'0')
	}
	return result, nil
}

func parseRumbleDate(text string) (time.Time, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, "unknown"
	}

	if parsed, err := time.Parse(time.RFC3339, text); err == nil {
		return parsed, "ok"
	}

	if parsed, err := time.Parse("Jan 2, 2006", text); err == nil {
		return parsed, "ok"
	}

	if strings.Contains(text, "ago") {
		return parseRelativeDate(text)
	}

	if strings.EqualFold(text, "yesterday") {
		return time.Now().UTC().Add(-24 * time.Hour), "ok"
	}

	return time.Time{}, "unknown"
}

func parseRelativeDate(text string) (time.Time, string) {
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return time.Time{}, "unknown"
	}
	amount, err := parseInt(parts[0])
	if err != nil {
		return time.Time{}, "unknown"
	}
	unit := strings.ToLower(parts[1])
	if strings.HasPrefix(unit, "minute") {
		return time.Now().UTC().Add(-time.Duration(amount) * time.Minute), "ok"
	}
	if strings.HasPrefix(unit, "hour") {
		return time.Now().UTC().Add(-time.Duration(amount) * time.Hour), "ok"
	}
	if strings.HasPrefix(unit, "day") {
		return time.Now().UTC().Add(-time.Duration(amount) * 24 * time.Hour), "ok"
	}
	if strings.HasPrefix(unit, "week") {
		return time.Now().UTC().Add(-time.Duration(amount) * 7 * 24 * time.Hour), "ok"
	}
	if strings.HasPrefix(unit, "month") {
		return time.Now().UTC().Add(-time.Duration(amount) * 30 * 24 * time.Hour), "ok"
	}
	if strings.HasPrefix(unit, "year") {
		return time.Now().UTC().Add(-time.Duration(amount) * 365 * 24 * time.Hour), "ok"
	}
	return time.Time{}, "unknown"
}

func firstText(s *goquery.Selection, selectors []string) string {
	for _, selector := range selectors {
		text := strings.TrimSpace(s.Find(selector).First().Text())
		if text != "" {
			return text
		}
	}
	return ""
}

func firstAttr(s *goquery.Selection, attr string, selectors []string) string {
	for _, selector := range selectors {
		value, ok := s.Find(selector).First().Attr(attr)
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func toAbsoluteURL(base string, link string) string {
	if link == "" {
		return ""
	}
	if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return link
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return link
	}
	ref, err := url.Parse(link)
	if err != nil {
		return link
	}
	return baseURL.ResolveReference(ref).String()
}
