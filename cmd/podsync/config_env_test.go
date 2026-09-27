package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const envBaseConfig = `
[server]
port = 8080

[feeds]
  [feeds.Doctrine]
  url = "https://www.youtube.com/@doctrine"
  page_size = 10
`

func TestLoadConfig_EnvOverrides(t *testing.T) {
	t.Setenv("PODSYNC__SERVER__PORT", "9000")
	t.Setenv("PODSYNC__SERVER__WEB_UI", "true")
	t.Setenv("PODSYNC__STORAGE__LOCAL__DATA_DIR", "/env/data")
	t.Setenv("PODSYNC__TOKENS__YOUTUBE", "key1 key2")
	t.Setenv("PODSYNC__FEEDS__DOCTRINE__PAGE_SIZE", "3")
	t.Setenv("PODSYNC__FEEDS__DOCTRINE__UPDATE_PERIOD", "2h")
	t.Setenv("PODSYNC__FEEDS__DOCTRINE__YOUTUBE_DL_ARGS", "--embed-thumbnail, --no-mtime")
	t.Setenv("PODSYNC__FEEDS__NEWSHOW__URL", "https://rumble.com/c/example")
	t.Setenv("podsync__audiobookshelf__podcast_root", "/podcasts")

	cfg, err := LoadConfig(writeConfigFile(t, "config.toml", envBaseConfig))
	require.NoError(t, err)

	assert.Equal(t, 9000, cfg.Server.Port)
	assert.True(t, cfg.Server.WebUIEnabled)
	assert.Equal(t, "/env/data", cfg.Storage.Local.DataDir)
	assert.Equal(t, StringSlice{"key1", "key2"}, cfg.Tokens["youtube"])
	assert.Equal(t, "/podcasts", cfg.Audiobookshelf.PodcastRoot, "prefix matching is case-insensitive")

	require.Contains(t, cfg.Feeds, "Doctrine", "existing feed IDs keep their spelling")
	doctrine := cfg.Feeds["Doctrine"]
	assert.Equal(t, 3, doctrine.PageSize)
	assert.Equal(t, 2*time.Hour, doctrine.UpdatePeriod)
	assert.Equal(t, []string{"--embed-thumbnail", "--no-mtime"}, doctrine.YouTubeDLArgs)
	assert.Equal(t, "https://www.youtube.com/@doctrine", doctrine.URL, "keys not overridden keep their file values")

	require.Contains(t, cfg.Feeds, "newshow", "new feed IDs are lowercase")
	assert.Equal(t, "https://rumble.com/c/example", cfg.Feeds["newshow"].URL)
}

func TestLoadConfig_EnvOverridesApplyToYAML(t *testing.T) {
	t.Setenv("PODSYNC__SERVER__PORT", "9001")
	cfg, err := LoadConfig(writeConfigFile(t, "config.yaml", "server:\n  port: 8080\n"))
	require.NoError(t, err)
	assert.Equal(t, 9001, cfg.Server.Port)
}

func TestLoadConfig_EnvOverrideStringsStayStrings(t *testing.T) {
	// A token that looks like a number must stay a string.
	t.Setenv("PODSYNC__TOKENS__VIMEO", "12345")
	t.Setenv("PODSYNC__FEEDS__DOCTRINE__CUSTOM__OWNERNAME", "007")

	cfg, err := LoadConfig(writeConfigFile(t, "config.toml", envBaseConfig))
	require.NoError(t, err)
	assert.Equal(t, StringSlice{"12345"}, cfg.Tokens["vimeo"])
	assert.Equal(t, "007", cfg.Feeds["Doctrine"].Custom.OwnerName)
}

func TestLoadConfig_LegacyAPIKeyEnvWins(t *testing.T) {
	t.Setenv("PODSYNC__TOKENS__YOUTUBE", "from-generic")
	t.Setenv("PODSYNC_YOUTUBE_API_KEY", "from-legacy")

	cfg, err := LoadConfig(writeConfigFile(t, "config.toml", envBaseConfig))
	require.NoError(t, err)
	assert.Equal(t, StringSlice{"from-legacy"}, cfg.Tokens["youtube"])
}

func TestLoadConfig_EnvOverrideErrors(t *testing.T) {
	tests := map[string]struct {
		name, value, want string
	}{
		"unknown option":     {"PODSYNC__SERVR__PORT", "9000", `"servr" is not a configuration option`},
		"unknown nested":     {"PODSYNC__FEEDS__DOCTRINE__PAGE_SIZ", "3", `"feeds.doctrine.page_siz" is not a configuration option`},
		"bad integer":        {"PODSYNC__SERVER__PORT", "eighty", "expected a whole number"},
		"bad bool":           {"PODSYNC__SERVER__TLS", "maybe", "expected true or false"},
		"below a value":      {"PODSYNC__SERVER__PORT__X", "1", "no nested options"},
		"empty segment":      {"PODSYNC__SERVER____PORT", "1", "empty path segment"},
		"table not settable": {"PODSYNC__FEEDS__DOCTRINE__SIGNATURE_RULES", "x", "cannot be set from the environment"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv(tt.name, tt.value)
			_, err := LoadConfig(writeConfigFile(t, "config.toml", envBaseConfig))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.name, "the error names the variable")
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
