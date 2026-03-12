package feed

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	itunes "github.com/eduncan911/podcast"
	"github.com/mxpv/podsync/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildXML(t *testing.T) {
	feed := model.Feed{
		Episodes: []*model.Episode{
			{
				ID:          "1",
				Status:      model.EpisodeDownloaded,
				Title:       "title",
				Description: "description",
			},
		},
	}

	cfg := Config{
		ID:     "test",
		Custom: Custom{Description: "description", Category: "Technology", Subcategories: []string{"Gadgets", "Podcasting"}},
	}

	out, err := Build(context.Background(), &feed, &cfg, "http://localhost/")
	assert.NoError(t, err)

	assert.EqualValues(t, "description", out.Description)
	assert.EqualValues(t, "Technology", out.Category)

	require.Len(t, out.ICategories, 1)
	category := out.ICategories[0]
	assert.EqualValues(t, "Technology", category.Text)

	require.Len(t, category.ICategories, 2)
	assert.EqualValues(t, "Gadgets", category.ICategories[0].Text)
	assert.EqualValues(t, "Podcasting", category.ICategories[1].Text)

	require.Len(t, out.Items, 1)
	require.NotNil(t, out.Items[0].Enclosure)
	assert.EqualValues(t, out.Items[0].Enclosure.URL, "http://localhost/test/1.mp4")
	assert.EqualValues(t, out.Items[0].Enclosure.Type, itunes.MP4)
}

func TestBuildXMLUsesRSSSummaryWithoutSubtitle(t *testing.T) {
	feed := model.Feed{
		Episodes: []*model.Episode{{
			ID:            "1",
			Status:        model.EpisodeDownloaded,
			Title:         "NYC ISIS Attack Proves Definitively - Islam & America are Incompatible",
			Subtitle:      "NYC ISIS Attack Proves Definitively - Islam & America are Incompatible",
			Description:   `Short summary & context`,
			Summary:       `Short summary & context`,
			Author:        "Steven Crowder",
			Keywords:      "news, politics & culture",
			Season:        2026,
			EpisodeNumber: 10,
			EpisodeType:   "full",
		}},
	}

	cfg := Config{ID: "test"}

	out, err := Build(context.Background(), &feed, &cfg, "http://localhost/")
	require.NoError(t, err)

	xmlText := out.String()
	assert.Contains(t, xmlText, `<title>NYC ISIS Attack Proves Definitively - Islam &amp; America are Incompatible</title>`)
	assert.Contains(t, xmlText, `<description>Short summary &amp; context</description>`)
	assert.NotContains(t, xmlText, `<itunes:subtitle>`)
	assert.Contains(t, xmlText, `<itunes:summary><![CDATA[Short summary & context]]></itunes:summary>`)
	assert.False(t, strings.Contains(xmlText, `&amp;amp;`))
	assert.NotContains(t, xmlText, `<itunes:keywords>`)
	assert.NotContains(t, xmlText, `<itunes:season>`)
	assert.NotContains(t, xmlText, `<itunes:episode>`)
	assert.NotContains(t, xmlText, `<itunes:episodeType>`)
}

func TestRenderXMLRewritesItemTextFieldsAsCDATA(t *testing.T) {
	feedModel := model.Feed{
		Format: model.FormatAudio,
		Episodes: []*model.Episode{{
			ID:          "v76ws1m",
			Status:      model.EpisodeDownloaded,
			Title:       "NYC ISIS Attack Proves Definitively - Islam & America are Incompatible",
			Subtitle:    "NYC ISIS Attack Proves Definitively - Islam & America are Incompatible",
			Description: "Democrat Senate candidate James Talarico can't seem to stop shilling for the Left.",
			Duration:    7098,
			Size:        35263725,
		}},
	}

	cfg := Config{ID: "crowder", Format: model.FormatAudio}
	podcast, err := Build(context.Background(), &feedModel, &cfg, "https://podsync.domain.com")
	require.NoError(t, err)

	xmlText, err := RenderXML(podcast, feedModel.Episodes)
	require.NoError(t, err)

	assert.Contains(t, xmlText, `<title><![CDATA[NYC ISIS Attack Proves Definitively - Islam & America are Incompatible]]></title>`)
	assert.Contains(t, xmlText, `<description><![CDATA[Democrat Senate candidate James Talarico can't seem to stop shilling for the Left.]]></description>`)
	assert.NotContains(t, xmlText, `<itunes:subtitle>`)
	assert.NotContains(t, xmlText, `Islam &amp; America`)
	assert.NotContains(t, xmlText, `can&#39;t`)
}

func TestRenderXMLIncludesExtendedRSSMetadataFields(t *testing.T) {
	feedModel := model.Feed{
		Format: model.FormatAudio,
		Episodes: []*model.Episode{{
			ID:            "1",
			Status:        model.EpisodeDownloaded,
			Title:         "NYC ISIS Attack Proves Definitively - Islam & America are Incompatible",
			Description:   "Democrat Senate candidate James Talarico was pitched as the moderate option.",
			Duration:      3712,
			Size:          35263725,
			Season:        2026,
			EpisodeNumber: 44,
			EpisodeType:   "full",
		}},
	}

	cfg := Config{ID: "crowder", Format: model.FormatAudio}
	podcast, err := Build(context.Background(), &feedModel, &cfg, "https://podsync.domain.com")
	require.NoError(t, err)

	xmlText, err := RenderXML(podcast, feedModel.Episodes)
	require.NoError(t, err)

	assert.Contains(t, xmlText, `<enclosure url="https://podsync.domain.com/crowder/1.mp3" length="35263725" type="audio/mpeg"></enclosure>`)
	assert.Contains(t, xmlText, `<itunes:duration>01:01:52</itunes:duration>`)
	assert.Contains(t, xmlText, "\n      <itunes:order>1</itunes:order>\n      <itunes:season>2026</itunes:season>\n      <itunes:episode>44</itunes:episode>\n      <itunes:episodeType>full</itunes:episodeType>\n    </item>")
	assert.Contains(t, xmlText, `<itunes:season>2026</itunes:season>`)
	assert.Contains(t, xmlText, `<itunes:episode>44</itunes:episode>`)
	assert.Contains(t, xmlText, `<itunes:episodeType>full</itunes:episodeType>`)
}

func TestRenderXMLMatchesGolden(t *testing.T) {
	feedModel := model.Feed{
		Format: model.FormatAudio,
		Episodes: []*model.Episode{{
			ID:            "1",
			Status:        model.EpisodeDownloaded,
			Title:         "NYC ISIS Attack Proves Definitively - Islam & America are Incompatible",
			Description:   "Democrat Senate candidate James Talarico was pitched as the moderate option.",
			Duration:      3712,
			Size:          35263725,
			Season:        2026,
			EpisodeNumber: 44,
			EpisodeType:   "full",
		}},
	}

	cfg := Config{ID: "crowder", Format: model.FormatAudio}
	podcast, err := Build(context.Background(), &feedModel, &cfg, "https://podsync.domain.com")
	require.NoError(t, err)
	xmlText, err := RenderXML(podcast, feedModel.Episodes)
	require.NoError(t, err)

	golden, err := os.ReadFile("testdata/rss_extended.golden.xml")
	require.NoError(t, err)
	normalize := func(value string) string {
		pubDateRe := regexp.MustCompile(`<pubDate>.*?</pubDate>`)
		lastBuildRe := regexp.MustCompile(`<lastBuildDate>.*?</lastBuildDate>`)
		value = strings.ReplaceAll(value, "\r\n", "\n")
		value = pubDateRe.ReplaceAllString(value, `<pubDate>__PUBDATE__</pubDate>`)
		value = lastBuildRe.ReplaceAllString(value, `<lastBuildDate>__LASTBUILD__</lastBuildDate>`)
		value = strings.TrimSpace(value)
		return value
	}
	assert.Equal(t, normalize(string(golden)), normalize(xmlText))
}

func TestRenderXMLMatchesPrivateChannelGolden(t *testing.T) {
	feedModel := model.Feed{
		Title:       "Private Feed Title",
		Description: "Private feed description",
		Author:      "Private Author",
		ItemURL:     "https://example.com/channel",
		PrivateFeed: true,
		Format:      model.FormatVideo,
		Episodes: []*model.Episode{{
			ID:          "private-1",
			Status:      model.EpisodeDownloaded,
			Title:       "Private Episode",
			Description: "Private episode description",
			Summary:     "Private episode summary",
			Duration:    125,
			Size:        2048,
		}},
	}

	cfg := Config{ID: "private", Format: model.FormatVideo}
	podcast, err := Build(context.Background(), &feedModel, &cfg, "https://podsync.domain.com")
	require.NoError(t, err)
	xmlText, err := RenderXML(podcast, feedModel.Episodes)
	require.NoError(t, err)

	golden, err := os.ReadFile("testdata/rss_channel_private.golden.xml")
	require.NoError(t, err)
	normalize := func(value string) string {
		pubDateRe := regexp.MustCompile(`<pubDate>.*?</pubDate>`)
		lastBuildRe := regexp.MustCompile(`<lastBuildDate>.*?</lastBuildDate>`)
		value = strings.ReplaceAll(value, "\r\n", "\n")
		value = pubDateRe.ReplaceAllString(value, `<pubDate>__PUBDATE__</pubDate>`)
		value = lastBuildRe.ReplaceAllString(value, `<lastBuildDate>__LASTBUILD__</lastBuildDate>`)
		value = strings.TrimSpace(value)
		return value
	}
	assert.Equal(t, normalize(string(golden)), normalize(xmlText))
}

func TestRenderXMLMatchesSubtitlelessGolden(t *testing.T) {
	feedModel := model.Feed{
		Format: model.FormatAudio,
		Episodes: []*model.Episode{{
			ID:          "subless",
			Status:      model.EpisodeDownloaded,
			Title:       "Subtitleless Episode",
			Subtitle:    "This should not be emitted",
			Description: "A clean description",
			Duration:    60,
			Size:        1000,
		}},
	}

	cfg := Config{ID: "subless", Format: model.FormatAudio}
	podcast, err := Build(context.Background(), &feedModel, &cfg, "https://podsync.domain.com")
	require.NoError(t, err)
	xmlText, err := RenderXML(podcast, feedModel.Episodes)
	require.NoError(t, err)
	assert.NotContains(t, xmlText, `<itunes:subtitle>`)
	assert.Contains(t, xmlText, `<title><![CDATA[Subtitleless Episode]]></title>`)
	assert.Contains(t, xmlText, `<description><![CDATA[A clean description]]></description>`)
}
