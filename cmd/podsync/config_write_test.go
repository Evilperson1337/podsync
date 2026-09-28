package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/configschema"
)

func testSchema() *configschema.Schema {
	return configschema.Generate(reflect.TypeOf(Config{}))
}

// decodeConfig decodes a rendered file into Config without validation (the example references
// files that do not exist here).
func decodeConfig(t *testing.T, format configFormat, data []byte) Config {
	t.Helper()
	tree, err := parseConfigTree(format, data)
	require.NoError(t, err, string(data))
	var cfg Config
	require.NoError(t, tree.Unmarshal(&cfg))
	assert.Empty(t, findUnknownConfigKeys(tree, reflect.TypeOf(cfg)))
	return cfg
}

func TestRenderConfigRoundTripsExample(t *testing.T) {
	data, err := os.ReadFile("../../config.toml.example")
	require.NoError(t, err)
	document, err := parseConfigDocument(configFormatTOML, data)
	require.NoError(t, err)
	original := decodeConfig(t, configFormatTOML, data)

	for _, format := range []configFormat{configFormatTOML, configFormatYAML, configFormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			rendered, err := renderConfig(format, document, testSchema())
			require.NoError(t, err)
			assert.Equal(t, original, decodeConfig(t, format, rendered), "the rendered file must configure Podsync identically")

			// Rendering is deterministic.
			again, err := renderConfig(format, document, testSchema())
			require.NoError(t, err)
			assert.Equal(t, string(rendered), string(again))
		})
	}
}

func TestRenderTOMLLayout(t *testing.T) {
	document := map[string]interface{}{
		"feeds": map[string]interface{}{
			"show": map[string]interface{}{
				"url":             "https://example.com",
				"page_size":       int64(5),
				"clean":           map[string]interface{}{"keep_last": int64(3)},
				"signature_rules": []interface{}{map[string]interface{}{"file": "a.wav", "action": "cut_before", "post": float64(60)}},
			},
		},
		"server": map[string]interface{}{"port": int64(8080)},
		"tokens": map[string]interface{}{"youtube": []interface{}{"a", "b"}},
	}
	out, err := renderConfig(configFormatTOML, document, testSchema())
	require.NoError(t, err)
	text := string(out)

	assert.True(t, strings.HasPrefix(text, "# Podsync configuration."))
	assert.Less(t, strings.Index(text, "[server]"), strings.Index(text, "[feeds]"), "sections follow schema order")
	assert.Less(t, strings.Index(text, "[feeds]"), strings.Index(text, "[tokens]"))
	assert.Contains(t, text, "# Port of the podcast web server (default 8080).\nport = 8080\n", "options carry their description")
	assert.Contains(t, text, "  [feeds.show.clean]\n")
	assert.Contains(t, text, "  [[feeds.show.signature_rules]]\n")
	assert.Contains(t, text, "post = 60.0", "floats stay floats")
	assert.Contains(t, text, `youtube = ["a", "b"]`)
	assert.NotContains(t, text, "# <id>", "map entries such as feed IDs get no description")
}

func TestRenderConfigTrickyValues(t *testing.T) {
	document := map[string]interface{}{
		"server": map[string]interface{}{"hostname": "https://pod.example.com/a b"},
		"feeds": map[string]interface{}{
			"my.feed": map[string]interface{}{
				"url": "quote \" backslash \\ newline \n tab \t unicode é ✓ control \x01",
				"custom": map[string]interface{}{
					"title":  "true",
					"author": "123",
					"lang":   "null",
					"link":   "- not a list",
				},
				"youtube_dl_args": []interface{}{"--foo", "a: b", "#hash"},
			},
		},
		"tokens": map[string]interface{}{},
	}
	for _, format := range []configFormat{configFormatTOML, configFormatYAML, configFormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			rendered, err := renderConfig(format, document, testSchema())
			require.NoError(t, err)
			cfg := decodeConfig(t, format, rendered)
			feed := cfg.Feeds["my.feed"]
			require.NotNil(t, feed, "keys with dots are quoted")
			assert.Equal(t, document["feeds"].(map[string]interface{})["my.feed"].(map[string]interface{})["url"], feed.URL)
			assert.Equal(t, "true", feed.Custom.Title, "strings that look like other types stay strings")
			assert.Equal(t, "123", feed.Custom.Author)
			assert.Equal(t, "null", feed.Custom.Language)
			assert.Equal(t, []string{"--foo", "a: b", "#hash"}, feed.YouTubeDLArgs)
			assert.NotNil(t, cfg.Tokens, "an empty table is preserved")
		})
	}
}

func TestRenderStarterConfigs(t *testing.T) {
	for _, format := range []configFormat{configFormatTOML, configFormatYAML, configFormatJSON} {
		path := "config." + string(format)
		document, err := parseConfigDocument(format, starterConfigFor(path))
		require.NoError(t, err)
		_, err = renderConfig(format, document, testSchema())
		require.NoError(t, err, string(format))
	}
}

func TestRenderConfigRejectsUnsupportedValues(t *testing.T) {
	_, err := renderConfig(configFormatTOML, map[string]interface{}{"server": map[string]interface{}{"port": struct{}{}}}, testSchema())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server.port")
}
