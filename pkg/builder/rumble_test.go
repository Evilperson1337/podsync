package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/mxpv/podsync/pkg/model"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestExtractRumbleGUID(t *testing.T) {
	cases := []struct {
		link string
		guid string
	}{
		{"https://rumble.com/v5abcde-title.html", "v5abcde"},
		{"https://rumble.com/v9xyz1", "v9xyz1"},
		{"https://rumble.com/c/somechannel", ""},
	}
	for _, tc := range cases {
		require.Equal(t, tc.guid, extractRumbleGUID(tc.link))
	}
}

func TestParseRumbleListing(t *testing.T) {
	fixture := filepath.Join("testdata", "rumble", "videos.html")
	data, err := os.ReadFile(fixture)
	require.NoError(t, err)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	require.NoError(t, err)

	builder := &RumbleBuilder{}
	items, next, err := builder.parseListing(doc, "https://rumble.com/c/Example", model.TypeChannel, log.New())
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.NotEmpty(t, items[0].episode.ID)
	require.NotEmpty(t, items[0].episode.Title)
	require.NotEmpty(t, next)
}

func TestParseRumbleListingLivestreams(t *testing.T) {
	fixture := filepath.Join("testdata", "rumble", "livestreams.html")
	data, err := os.ReadFile(fixture)
	require.NoError(t, err)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	require.NoError(t, err)

	builder := &RumbleBuilder{}
	items, _, err := builder.parseListing(doc, "https://rumble.com/c/Example/livestreams", model.TypeLivestreams, log.New())
	require.NoError(t, err)
	require.NotEmpty(t, items)
	require.Equal(t, "v9done1", items[0].episode.ID)
}

func TestParseDuration(t *testing.T) {
	require.EqualValues(t, 754, parseDuration("12:34"))
	require.EqualValues(t, 3723, parseDuration("1:02:03"))
}

func TestExtractNextPageURL(t *testing.T) {
	fixture := filepath.Join("testdata", "rumble", "pagination.html")
	data, err := os.ReadFile(fixture)
	require.NoError(t, err)

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(data)))
	require.NoError(t, err)

	next := extractNextPageURL(doc, "https://rumble.com/c/Example")
	require.Equal(t, "https://rumble.com/c/Example?page=3", next)
}

func TestParseRumbleDateFallback(t *testing.T) {
	parsed, status := parseRumbleDate("2 hours ago")
	require.Equal(t, "ok", status)
	require.WithinDuration(t, time.Now().UTC().Add(-2*time.Hour), parsed, 2*time.Minute)

	parsed, status = parseRumbleDate("")
	require.Equal(t, "unknown", status)
	require.True(t, parsed.IsZero())
}
