package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/fs"
)

func TestRequiresSignatureTooling(t *testing.T) {
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")

	newConfig := func(dataDir string) *Config {
		return &Config{
			Storage: fs.Config{Type: "local", Local: fs.LocalConfig{DataDir: dataDir}},
			Feeds:   map[string]*feed.Config{"doctrine": {ID: "doctrine"}},
		}
	}

	t.Run("no rules in default location", func(t *testing.T) {
		assert.False(t, requiresSignatureTooling(newConfig(t.TempDir())))
	})

	t.Run("rules in default data directory location", func(t *testing.T) {
		dataDir := t.TempDir()
		sigDir := filepath.Join(dataDir, "doctrine", "signatures")
		require.NoError(t, os.MkdirAll(sigDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sigDir, "rules.json"), []byte(`{"rules":[]}`), 0644))

		assert.True(t, requiresSignatureTooling(newConfig(dataDir)))
	})

	t.Run("explicit root_dir", func(t *testing.T) {
		cfg := newConfig(t.TempDir())
		cfg.Signatures.RootDir = t.TempDir()
		assert.True(t, requiresSignatureTooling(cfg))
	})

	t.Run("sponsorblock enabled", func(t *testing.T) {
		cfg := newConfig(t.TempDir())
		cfg.Feeds["doctrine"].Custom.SponsorBlock = feed.SponsorBlock{Enabled: true}
		assert.True(t, requiresSignatureTooling(cfg))
	})

	t.Run("s3 storage without explicit root", func(t *testing.T) {
		cfg := newConfig("")
		cfg.Storage.Type = "s3"
		assert.False(t, requiresSignatureTooling(cfg))
	})
}
