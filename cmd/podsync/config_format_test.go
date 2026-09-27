package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeConfigFile writes content to name inside a new temp directory and returns its path.
func writeConfigFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestConfigFormatFor(t *testing.T) {
	assert.Equal(t, configFormatTOML, configFormatFor("config.toml"))
	assert.Equal(t, configFormatTOML, configFormatFor("/etc/podsync/config"))
	assert.Equal(t, configFormatYAML, configFormatFor("config.yaml"))
	assert.Equal(t, configFormatYAML, configFormatFor("CONFIG.YML"))
	assert.Equal(t, configFormatJSON, configFormatFor("config.json"))
}

func TestLoadConfig_FormatsAreEquivalent(t *testing.T) {
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")
	signature := filepath.Join(t.TempDir(), "intro.wav")
	require.NoError(t, os.WriteFile(signature, []byte("RIFF"), 0644))

	tomlDoc := `
[server]
port = 9090
hostname = "https://pod.example.com"

[storage]
type = "local"
  [storage.local]
  data_dir = "/data/podsync"

[tokens]
youtube = "key-a"
vimeo = ["key-b", "key-c"]

[cleanup]
keep_last = 25

[downloader]
timeout = 30

[feeds]
  [feeds.show]
  url = "https://www.youtube.com/@example"
  format = "audio"
  update_period = "6h"
  page_size = 5
  clean = { keep_last = 3 }
  filters = { title = "Episode", min_duration = 60 }
  youtube_dl_args = ["--embed-thumbnail"]

  [[feeds.show.post_episode_download]]
  command = ["echo", "done"]
  timeout = 10

  [[feeds.show.signature_rules]]
  file = "` + filepath.ToSlash(signature) + `"
  action = "remove_segment"
  pre = 0
  post = 1.5
  max_matches = 4

  [feeds.show.custom]
  ownerName = "Owner"
  explicit = true
    [feeds.show.custom.sponsorblock]
    enabled = true
    categories = ["sponsor", "intro"]
`
	yamlDoc := `
server:
  port: 9090
  hostname: https://pod.example.com
storage:
  type: local
  local:
    data_dir: /data/podsync
tokens:
  youtube: key-a
  vimeo: [key-b, key-c]
cleanup:
  keep_last: 25
downloader:
  timeout: 30
feeds:
  show:
    url: https://www.youtube.com/@example
    format: audio
    update_period: 6h
    page_size: 5
    clean: {keep_last: 3}
    filters: {title: Episode, min_duration: 60}
    youtube_dl_args: ["--embed-thumbnail"]
    post_episode_download:
      - command: [echo, done]
        timeout: 10
    signature_rules:
      - file: "` + filepath.ToSlash(signature) + `"
        action: remove_segment
        pre: 0
        post: 1.5
        max_matches: 4
    custom:
      ownerName: Owner
      explicit: true
      sponsorblock:
        enabled: true
        categories: [sponsor, intro]
`
	jsonDoc := `{
  "server": {"port": 9090, "hostname": "https://pod.example.com"},
  "storage": {"type": "local", "local": {"data_dir": "/data/podsync"}},
  "tokens": {"youtube": "key-a", "vimeo": ["key-b", "key-c"]},
  "cleanup": {"keep_last": 25},
  "downloader": {"timeout": 30},
  "feeds": {
    "show": {
      "url": "https://www.youtube.com/@example",
      "format": "audio",
      "update_period": "6h",
      "page_size": 5,
      "clean": {"keep_last": 3},
      "filters": {"title": "Episode", "min_duration": 60},
      "youtube_dl_args": ["--embed-thumbnail"],
      "post_episode_download": [{"command": ["echo", "done"], "timeout": 10}],
      "signature_rules": [{"file": "` + filepath.ToSlash(signature) + `", "action": "remove_segment", "pre": 0, "post": 1.5, "max_matches": 4}],
      "custom": {"ownerName": "Owner", "explicit": true, "sponsorblock": {"enabled": true, "categories": ["sponsor", "intro"]}}
    }
  }
}`

	// All three live in one directory so path-derived defaults (database dir) match.
	dir := t.TempDir()
	load := func(name, content string) *Config {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
		cfg, err := LoadConfig(path)
		require.NoError(t, err, name)
		return cfg
	}
	fromTOML := load("config.toml", tomlDoc)
	fromYAML := load("config.yaml", yamlDoc)
	fromJSON := load("config.json", jsonDoc)

	assert.Equal(t, fromTOML, fromYAML, "YAML must decode exactly like TOML")
	assert.Equal(t, fromTOML, fromJSON, "JSON must decode exactly like TOML")

	show := fromYAML.Feeds["show"]
	assert.Equal(t, 6*time.Hour, show.UpdatePeriod)
	assert.Equal(t, StringSlice{"key-b", "key-c"}, fromJSON.Tokens["vimeo"])
	assert.EqualValues(t, 1.5, show.SignatureRules[0].PostSeconds)
	assert.True(t, show.Custom.SponsorBlockConfig().Enabled)
}

func TestLoadConfig_YAMLAndJSONRejectUnknownKeys(t *testing.T) {
	yamlPath := writeConfigFile(t, "config.yaml", "server:\n  prot: 8080\nfeeds:\n  show:\n    url: https://example.com\n    signature_rules:\n      - file: a.wav\n        acton: cut_before\n")
	_, err := LoadConfig(yamlPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server.prot")
	assert.Contains(t, err.Error(), "feeds.show.signature_rules[0].acton")

	jsonPath := writeConfigFile(t, "config.json", `{"sever": {"port": 8080}}`)
	_, err = LoadConfig(jsonPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sever")
}

func TestLoadConfig_YAMLAndJSONErrors(t *testing.T) {
	_, err := LoadConfig(writeConfigFile(t, "config.yaml", "server:\n  port: [unclosed\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "as YAML")

	_, err = LoadConfig(writeConfigFile(t, "config.json", `{"server": {"port": 8080,}}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "as JSON")

	_, err = LoadConfig(writeConfigFile(t, "config.json", `{"feeds": {"a": {"url": "x", "page_size": 10.5}}}`))
	require.Error(t, err, "a fractional number is not accepted for a whole-number option")

	_, err = LoadConfig(writeConfigFile(t, "config.yaml", "- just\n- a list\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a mapping")
}

func TestLoadConfig_EmptyYAMLAndNulls(t *testing.T) {
	cfg, err := LoadConfig(writeConfigFile(t, "config.yaml", "# only comments\n"))
	require.NoError(t, err)
	assert.Empty(t, cfg.Feeds)

	cfg, err = LoadConfig(writeConfigFile(t, "config.yaml", "tokens:\nfeeds:\n  show:\n    url: https://example.com\n    page_size: ~\n"))
	require.NoError(t, err, "null values mean 'not set'")
	assert.Equal(t, 50, cfg.Feeds["show"].PageSize, "default applies when the value is null")
}

func TestStarterConfigsLoadInEveryFormat(t *testing.T) {
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")
	for _, name := range []string{"config.toml", "config.yaml", "config.yml", "config.json"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			require.NoError(t, writeStarterConfig(path))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, starterConfigFor(path), data)

			cfg, err := LoadConfig(path)
			require.NoError(t, err)
			assert.Empty(t, cfg.Feeds)
			assert.Equal(t, 8080, cfg.Server.Port)
			assert.Equal(t, filepath.Join(filepath.Dir(path), "data"), cfg.Storage.Local.DataDir)
		})
	}
	assert.True(t, strings.HasPrefix(string(starterConfigFor("x.yaml")), "# Podsync configuration."))
}
