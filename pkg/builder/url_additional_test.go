package builder

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/model"
)

func TestParseURLNormalizesHostsAndPaths(t *testing.T) {
	tests := []struct {
		url      string
		provider model.Provider
		kind     model.Type
		id       string
	}{
		{url: "https://m.youtube.com/@test/videos", provider: model.ProviderYoutube, kind: model.TypeHandle, id: "test"},
		{url: "https://mobile.youtube.com/channel/abc/videos", provider: model.ProviderYoutube, kind: model.TypeChannel, id: "abc"},
		{url: "https://www.twitch.tv/testuser/", provider: model.ProviderTwitch, kind: model.TypeUser, id: "testuser"},
		{url: "https://vimeo.com/channels/staffpicks/", provider: model.ProviderVimeo, kind: model.TypeChannel, id: "staffpicks"},
		{url: "https://soundcloud.com/user/sets/example-set/", provider: model.ProviderSoundcloud, kind: model.TypePlaylist, id: "example-set"},
		{url: "https://rumble.com/c/TestChannel/livestreams/", provider: model.ProviderRumble, kind: model.TypeLivestreams, id: "TestChannel"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			info, err := ParseURL(tt.url)
			require.NoError(t, err)
			require.Equal(t, tt.provider, info.Provider)
			require.Equal(t, tt.kind, info.LinkType)
			require.Equal(t, tt.id, info.ItemID)
		})
	}
}
