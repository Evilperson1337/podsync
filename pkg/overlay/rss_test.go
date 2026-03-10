package overlay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

func TestRSSProviderDisabledWithoutURL(t *testing.T) {
	provider := NewRSSProvider(nil)
	result := sampleFeed()
	original := result.Episodes[0].Title

	err := provider.Apply(context.Background(), &feed.Config{ID: "test"}, result)
	require.NoError(t, err)
	assert.Equal(t, original, result.Episodes[0].Title)
}

func TestNormalizeTitleHTMLAndQuotes(t *testing.T) {
	left := normalizeTitle("The Trump Protesting Marine Isn't Who You Think He Is")
	right := normalizeTitle("The Trump Protesting Marine Isn&#39;t Who You Think He Is")
	assert.Equal(t, left, right)
}

func TestRSSProviderOverlayMatchingAndPrecedence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" xmlns:content="http://purl.org/rss/1.0/modules/content/">
	<channel>
		<item>
			<title>The Trump Protesting Marine Isn&#39;t Who You Think He Is</title>
			<itunes:title>The Trump Protesting Marine Isn't Who You Think He Is</itunes:title>
			<link>https://example.com/episode-1</link>
			<description>Short summary</description>
			<content:encoded><![CDATA[<p>Rich description</p>]]></content:encoded>
			<pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate>
			<itunes:author>Overlay Author</itunes:author>
			<itunes:explicit>yes</itunes:explicit>
			<itunes:duration>9999</itunes:duration>
			<enclosure url="https://example.com/rss.mp3" length="777" type="audio/mpeg" />
		</item>
	</channel>
</rss>`))
	}))
	defer server.Close()

	provider := NewRSSProvider(server.Client())
	result := sampleFeed()

	err := provider.Apply(context.Background(), &feed.Config{ID: "test", Custom: feed.Custom{RSSMetadataURL: server.URL}}, result)
	require.NoError(t, err)

	episode := result.Episodes[0]
	assert.Equal(t, "The Trump Protesting Marine Isn't Who You Think He Is", episode.Title)
	assert.Equal(t, "<p>Rich description</p>", episode.Description)
	assert.Equal(t, "https://example.com/episode-1", episode.Link)
	assert.Equal(t, "Overlay Author", episode.Author)
	assert.NotNil(t, episode.Explicit)
	assert.True(t, *episode.Explicit)
	assert.EqualValues(t, 321, episode.Duration)
	assert.Equal(t, "https://rumble.com/v123abc-title.html", episode.VideoURL)
	assert.Equal(t, rssMetadataSource, episode.MetadataSource)
	assert.Equal(t, rssOrderSource, episode.OrderSource)
}

func TestRSSProviderFuzzyMatch(t *testing.T) {
	items := []*rssItem{{canonicalTitle: "Episode 101 The Big Interview Extended", normalizedTitle: normalizeTitle("Episode 101 The Big Interview Extended"), Order: 1}}
	episode := &model.Episode{Title: "Episode 101: The Big Interview - Extended"}
	match := chooseFuzzyMatch(episode, items, map[*rssItem]struct{}{})
	require.NotNil(t, match)
	assert.Equal(t, "fuzzy_title", match.strategy)
	assert.GreaterOrEqual(t, match.similarity, rssFuzzyThreshold)
}

func TestRSSProviderDateFallbackWithinThreshold(t *testing.T) {
	pubDate := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	items := []*rssItem{{canonicalTitle: "Episode ninety nine recap", normalizedTitle: normalizeTitle("Episode ninety nine recap"), PubDate: pubDate.Add(2 * time.Hour), Order: 1}}
	episode := &model.Episode{Title: "Episode 99 recap", PubDate: pubDate}
	match := chooseDateMatch(episode, items, map[*rssItem]struct{}{})
	require.NotNil(t, match)
	assert.Equal(t, "publish_date", match.strategy)
	assert.Equal(t, 2*time.Hour, match.dateDiff)
}

func TestRSSProviderNoWeakMatch(t *testing.T) {
	items := []*rssItem{{canonicalTitle: "Completely Different Show", normalizedTitle: normalizeTitle("Completely Different Show"), PubDate: time.Now().UTC(), Order: 1}}
	episode := &model.Episode{Title: "Unrelated Episode", PubDate: time.Now().UTC()}
	assert.Nil(t, chooseFuzzyMatch(episode, items, map[*rssItem]struct{}{}))
	assert.Nil(t, chooseDateMatch(episode, items, map[*rssItem]struct{}{}))
}

func TestRSSProviderFailureFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	provider := NewRSSProvider(server.Client())
	result := sampleFeed()
	original := result.Episodes[0].Title

	err := provider.Apply(context.Background(), &feed.Config{ID: "test", Custom: feed.Custom{RSSMetadataURL: server.URL}}, result)
	require.NoError(t, err)
	assert.Equal(t, original, result.Episodes[0].Title)
	assert.Empty(t, result.Episodes[0].MetadataSource)
}

func TestRSSProviderMalformedFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss><channel><item><title>broken`))
	}))
	defer server.Close()

	provider := NewRSSProvider(server.Client())
	result := sampleFeed()
	original := result.Episodes[0].Title

	err := provider.Apply(context.Background(), &feed.Config{ID: "test", Custom: feed.Custom{RSSMetadataURL: server.URL}}, result)
	require.NoError(t, err)
	assert.Equal(t, original, result.Episodes[0].Title)
}

func TestRSSOrderingOverridesMatchedItemsAndKeepsUnmatchedDeterministic(t *testing.T) {
	episodes := []*model.Episode{
		{ID: "one", Title: "First", PubDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{ID: "two", Title: "Second", PubDate: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{ID: "three", Title: "Third", PubDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	matches := map[string]*rssMatch{
		"two": {item: &rssItem{Order: 1}},
		"one": {item: &rssItem{Order: 2}},
	}

	applyRSSOrdering(episodes, matches, log.New())

	assert.Equal(t, "1", episodes[1].Order)
	assert.Equal(t, "2", episodes[0].Order)
	assert.Equal(t, "3", episodes[2].Order)
	assert.Equal(t, rssOrderSource, episodes[2].OrderSource)
}

func sampleFeed() *model.Feed {
	return &model.Feed{Episodes: []*model.Episode{{
		ID:       "v123abc",
		Title:    "The Trump Protesting Marine Isn't Who You Think He Is",
		Duration: 321,
		VideoURL: "https://rumble.com/v123abc-title.html",
		PubDate:  time.Date(2006, 1, 2, 16, 4, 5, 0, time.FixedZone("MST", -7*3600)).UTC(),
	}}}
}
