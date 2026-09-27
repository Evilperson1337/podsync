package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteStarterConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	require.NoError(t, writeStarterConfig(path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, starterConfig, data)

	err = writeStarterConfig(path)
	require.Error(t, err, "an existing file must never be overwritten")
	assert.Contains(t, err.Error(), "already exists")

	err = writeStarterConfig(filepath.Join(dir, "missing", "config.toml"))
	require.Error(t, err, "missing directories are not created")
}

func TestStarterConfigLoads(t *testing.T) {
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, writeStarterConfig(path))

	cfg, err := LoadConfig(path)
	require.NoError(t, err, "the starter configuration must pass strict validation")
	assert.Empty(t, cfg.Feeds)
	assert.Equal(t, "local", cfg.Storage.Type)
	assert.Equal(t, filepath.Join(dir, "data"), cfg.Storage.Local.DataDir)
	assert.Equal(t, filepath.Join(dir, "db"), cfg.Database.Dir)
	assert.Equal(t, 8080, cfg.Server.Port)
}

func TestLoadStartupConfig(t *testing.T) {
	t.Run("creates a starter config when missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")

		cfg, created, err := loadStartupConfig(path)
		require.NoError(t, err)
		assert.True(t, created)
		assert.Empty(t, cfg.Feeds)
		_, err = os.Stat(path)
		assert.NoError(t, err)

		_, created, err = loadStartupConfig(path)
		require.NoError(t, err)
		assert.False(t, created, "an existing file is loaded, not recreated")
	})

	t.Run("explains how to fix a missing directory", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-mounted", "config.toml")

		_, created, err := loadStartupConfig(path)
		require.Error(t, err)
		assert.False(t, created)
		assert.Contains(t, err.Error(), "podsync --init --config")
		assert.Contains(t, err.Error(), "could not be created automatically")
	})

	t.Run("invalid config is reported, not replaced", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		require.NoError(t, os.WriteFile(path, []byte("[sever]\nport = 1\n"), 0644))

		_, created, err := loadStartupConfig(path)
		require.Error(t, err)
		assert.False(t, created)
		assert.Contains(t, err.Error(), "sever")
	})
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "config.toml"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConfigNotFound))
}

func TestRunCheckConfig(t *testing.T) {
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")
	dir := t.TempDir()
	valid := filepath.Join(dir, "config.toml")
	require.NoError(t, writeStarterConfig(valid))
	invalid := filepath.Join(dir, "invalid.toml")
	require.NoError(t, os.WriteFile(invalid, []byte("[feeds]\n  [feeds.a]\n  urll = \"x\"\n"), 0644))

	assert.Equal(t, 0, runCheckConfig(context.Background(), valid))
	assert.Equal(t, 1, runCheckConfig(context.Background(), invalid))
	assert.Equal(t, 1, runCheckConfig(context.Background(), filepath.Join(dir, "missing.toml")))
	_, err := os.Stat(filepath.Join(dir, "missing.toml"))
	assert.True(t, os.IsNotExist(err), "--check-config never creates files")
}

func TestLoadConfig_DataDirDefaults(t *testing.T) {
	t.Run("defaults next to the config file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		require.NoError(t, os.WriteFile(path, []byte("[server]\nport = 8080\n"), 0644))

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, "data"), cfg.Storage.Local.DataDir)
	})

	t.Run("deprecated server.data_dir still wins", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		require.NoError(t, os.WriteFile(path, []byte("[server]\ndata_dir = \"/legacy\"\n"), 0644))

		cfg, err := LoadConfig(path)
		require.NoError(t, err)
		assert.Equal(t, "/legacy", cfg.Storage.Local.DataDir)
	})
}
