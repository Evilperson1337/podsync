package main

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/pelletier/go-toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfigExampleIsValid keeps config.toml.example in sync with the configuration structs:
// every key in it must be a real option, and it must decode. Full validation is not run because
// the example references files (certificates, signatures) that do not exist here.
func TestConfigExampleIsValid(t *testing.T) {
	data, err := os.ReadFile("../../config.toml.example")
	require.NoError(t, err)

	tree, err := toml.LoadBytes(data)
	require.NoError(t, err)
	var cfg Config
	require.NoError(t, tree.Unmarshal(&cfg))
	assert.Empty(t, findUnknownConfigKeys(tree, reflect.TypeOf(cfg)), "config.toml.example uses keys that no option reads")

	feed := cfg.Feeds["ID1"]
	require.NotNil(t, feed)
	assert.Equal(t, 12*time.Hour, feed.UpdatePeriod)
	assert.Len(t, feed.SignatureRules, 2)
	assert.True(t, feed.Custom.SponsorBlockConfig().Enabled, "the SponsorBlock example must actually enable SponsorBlock")
	assert.True(t, feed.Audiobookshelf.Enabled)
	require.Len(t, feed.PostEpisodeDownload, 2)
	assert.NoError(t, validateHooks("ID1", feed.PostEpisodeDownload, "post_episode_download"))
	assert.Len(t, cfg.Tokens["vimeo"], 2)
}
