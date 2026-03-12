package builder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseURLProviderContractCoverage(t *testing.T) {
	tests := []struct {
		url      string
		provider string
		itemID   string
	}{
		{url: "https://www.youtube.com/channel/abc", provider: "youtube", itemID: "abc"},
		{url: "https://vimeo.com/channels/staffpicks", provider: "vimeo", itemID: "staffpicks"},
		{url: "https://soundcloud.com/user/sets/example-set", provider: "soundcloud", itemID: "example-set"},
		{url: "https://twitch.tv/testuser", provider: "twitch", itemID: "testuser"},
		{url: "https://rumble.com/c/TestChannel", provider: "rumble", itemID: "TestChannel"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			info, err := ParseURL(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.provider, string(info.Provider))
			assert.Equal(t, tt.itemID, info.ItemID)
			assert.NotEmpty(t, info.LinkType)
		})
	}
}
