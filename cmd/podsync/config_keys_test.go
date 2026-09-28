package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_RejectsUnknownKeys(t *testing.T) {
	const header = `
[storage]
  [storage.local]
  data_dir = "/data"
`
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "typo in top-level section",
			body: `
[sever]
port = 8080

[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40&list=PL123"
`,
			want: []string{"sever (line 6)"},
		},
		{
			name: "typo in feed key and inline table",
			body: `
[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40&list=PL123"
  page_sise = 10
  filters = { titel = "x" }
`,
			want: []string{"feeds.A.page_sise (line 9)", "feeds.A.filters.titel"},
		},
		{
			name: "singular array-of-tables name",
			body: `
[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40&list=PL123"
  [[feeds.A.signature_rule]]
  file = "intro.wav"
  action = "cut_before"
`,
			want: []string{"feeds.A.signature_rule"},
		},
		{
			name: "typo inside array of tables",
			body: `
[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40&list=PL123"
  [[feeds.A.post_episode_download]]
  comand = ["echo"]
`,
			want: []string{"feeds.A.post_episode_download[0].comand"},
		},
		{
			name: "misplaced section",
			body: `
[feeds]
  [feeds.A]
  url = "https://youtube.com/watch?v=ygIUF678y40&list=PL123"
  [feeds.A.storage]
  type = "local"
`,
			want: []string{"feeds.A.storage"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := setup(t, header+tt.body)
			defer os.Remove(path)

			_, err := LoadConfig(path)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "unknown configuration keys")
			for _, want := range tt.want {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

func TestLoadConfig_AcceptsFreeFormKeys(t *testing.T) {
	path := setup(t, `
[storage]
  [storage.local]
  data_dir = "/data"

[tokens]
youtube = "a"
vimeo = ["b", "c"]

[feeds]
  [feeds.any_feed-ID]
  url = "https://youtube.com/watch?v=ygIUF678y40&list=PL123"
  update_period = "12h"
  [feeds.any_feed-ID.custom]
  ownerName = "Case Sensitive Tag"
  [feeds.ANOTHER]
  URL = "https://youtube.com/watch?v=ygIUF678y40&list=PL456"
`)
	defer os.Remove(path)

	config, err := LoadConfig(path)
	require.NoError(t, err)
	assert.Len(t, config.Feeds, 2)
	assert.Equal(t, "Case Sensitive Tag", config.Feeds["any_feed-ID"].Custom.OwnerName)
	assert.NotEmpty(t, config.Feeds["ANOTHER"].URL, "keys are matched case-insensitively like the decoder")
}
