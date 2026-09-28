package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/services/admin"
)

const storeBaseConfig = `# Hand-written comment that the admin interface will not preserve.
[server]
port = 8080

[tokens]
youtube = "secret-key"

[feeds]
  [feeds.show]
  url = "https://rumble.com/c/show"
`

type storeFixture struct {
	store   *fileConfigStore
	path    string
	updater *fakeFeedUpdater
}

func newStoreFixture(t *testing.T, content string) *storeFixture {
	t.Helper()
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")
	reloader, updater, _, path := newTestReloader(t, content)
	reloader.startup = reloader.current
	reloader.appliedHash = configContentHash([]byte(content))
	store := newFileConfigStore(path, testSchema(), reloader, nil)
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}
	store.environ = func() []string { return nil }
	return &storeFixture{store: store, path: path, updater: updater}
}

func (f *storeFixture) load(t *testing.T) admin.ConfigSnapshot {
	t.Helper()
	snapshot, err := f.store.Load()
	require.NoError(t, err)
	return snapshot
}

func TestConfigStoreLoad(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	fx.store.environ = func() []string {
		return []string{"PODSYNC__SERVER__PORT=9000", "PODSYNC_VIMEO_API_KEY=v", "PODSYNC__NOT__REAL=1", "HOME=/root"}
	}
	snapshot := fx.load(t)

	assert.Equal(t, "toml", snapshot.Format)
	assert.Equal(t, configContentHash([]byte(storeBaseConfig)), snapshot.Version)
	assert.Equal(t, int64(8080), snapshot.Document["server"].(map[string]interface{})["port"])
	assert.Equal(t, "secret-key", snapshot.Document["tokens"].(map[string]interface{})["youtube"], "the store returns raw values; the admin server masks them")
	assert.Equal(t, []admin.EnvOverride{
		{Path: []string{"tokens", "vimeo"}, Variable: "PODSYNC_VIMEO_API_KEY"},
		{Path: []string{"server", "port"}, Variable: "PODSYNC__SERVER__PORT"},
	}, snapshot.EnvOverrides, "sorted by variable; unknown override paths are left out")
	assert.Empty(t, snapshot.PendingRestart)
}

func TestConfigStoreValidate(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	document := fx.load(t).Document

	validation, err := fx.store.Validate(document)
	require.NoError(t, err)
	assert.True(t, validation.Valid, validation.Errors)
	assert.Contains(t, validation.Preview, "[feeds.show]")
	assert.Empty(t, validation.RestartRequired)

	document["server"].(map[string]interface{})["port"] = int64(9000)
	validation, err = fx.store.Validate(document)
	require.NoError(t, err)
	assert.Equal(t, []string{"server"}, validation.RestartRequired)

	delete(document["feeds"].(map[string]interface{})["show"].(map[string]interface{}), "url")
	document["feeds"].(map[string]interface{})["other"] = map[string]interface{}{"url": "u", "cron_schedule": "bad"}
	validation, err = fx.store.Validate(document)
	require.NoError(t, err)
	assert.False(t, validation.Valid)
	assert.Len(t, validation.Errors, 2, "each problem is reported separately: %v", validation.Errors)

	validation, err = fx.store.Validate(map[string]interface{}{"sever": map[string]interface{}{}})
	require.NoError(t, err)
	assert.False(t, validation.Valid)
	assert.Contains(t, strings.Join(validation.Errors, " "), "sever")
}

func TestConfigStoreSave(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	snapshot := fx.load(t)
	feeds := snapshot.Document["feeds"].(map[string]interface{})
	feeds["second"] = map[string]interface{}{"url": "https://rumble.com/c/second", "page_size": int64(3)}

	result, err := fx.store.Save(snapshot.Document, snapshot.Version, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"second"}, result.FeedsAdded, "the saved file is applied immediately")
	assert.Len(t, fx.updater.feeds, 2)

	written, err := os.ReadFile(fx.path)
	require.NoError(t, err)
	assert.Equal(t, result.Version, configContentHash(written))
	assert.Contains(t, string(written), "Managed by the Podsync admin interface")
	assert.Contains(t, string(written), `youtube = "secret-key"`)

	cfg, err := LoadConfig(fx.path)
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.Feeds["second"].PageSize)

	// The previous file, with its hand-written comment, is kept privately.
	require.NotEmpty(t, result.Backup)
	backupPath := filepath.Join(filepath.Dir(fx.path), result.Backup)
	backup, err := os.ReadFile(backupPath)
	require.NoError(t, err)
	assert.Equal(t, storeBaseConfig, string(backup))
	if runtime.GOOS != "windows" {
		info, err := os.Stat(backupPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestConfigStoreSaveConflict(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	snapshot := fx.load(t)
	require.NoError(t, os.WriteFile(fx.path, []byte(storeBaseConfig+"\n# hand edit\n"), 0644))

	_, err := fx.store.Save(snapshot.Document, snapshot.Version, "alice")
	require.ErrorIs(t, err, admin.ErrConflict)
	data, _ := os.ReadFile(fx.path)
	assert.Contains(t, string(data), "# hand edit", "the hand edit is not overwritten")
	backups, _ := fx.store.Backups()
	assert.Empty(t, backups)
}

func TestConfigStoreSaveInvalidWritesNothing(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	snapshot := fx.load(t)
	snapshot.Document["server"].(map[string]interface{})["port"] = "eighty"

	result, err := fx.store.Save(snapshot.Document, snapshot.Version, "alice")
	require.ErrorIs(t, err, admin.ErrInvalid)
	assert.False(t, result.Validation.Valid)
	assert.NotEmpty(t, result.Validation.Errors)
	data, _ := os.ReadFile(fx.path)
	assert.Equal(t, storeBaseConfig, string(data))
}

func TestConfigStoreUnchangedSaveDoesNotWrite(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	snapshot := fx.load(t)
	first, err := fx.store.Save(snapshot.Document, snapshot.Version, "alice")
	require.NoError(t, err)

	second, err := fx.store.Save(fx.load(t).Document, first.Version, "alice")
	require.NoError(t, err)
	assert.Empty(t, second.Backup, "saving identical content makes no backup")
	assert.Equal(t, first.Version, second.Version)
}

func TestConfigStoreBackupsArePruned(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	fx.store.maxBackups = 3
	for i := 0; i < 5; i++ {
		snapshot := fx.load(t)
		snapshot.Document["server"].(map[string]interface{})["port"] = int64(9000 + i)
		_, err := fx.store.Save(snapshot.Document, snapshot.Version, "alice")
		require.NoError(t, err)
	}
	backups, err := fx.store.Backups()
	require.NoError(t, err)
	require.Len(t, backups, 3)
	assert.True(t, backups[0].CreatedAt.After(backups[2].CreatedAt), "newest first")
}

func TestConfigStoreRestore(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	snapshot := fx.load(t)
	snapshot.Document["server"].(map[string]interface{})["port"] = int64(9000)
	saved, err := fx.store.Save(snapshot.Document, snapshot.Version, "alice")
	require.NoError(t, err)

	restored, err := fx.store.Restore(saved.Backup, saved.Version, "bob")
	require.NoError(t, err)
	data, err := os.ReadFile(fx.path)
	require.NoError(t, err)
	assert.Equal(t, storeBaseConfig, string(data), "a restore writes the backup's exact bytes, comments included")
	assert.Equal(t, configContentHash(data), restored.Version)
	assert.NotEmpty(t, restored.Backup, "the replaced version is backed up too")

	_, err = fx.store.Restore("../../etc/passwd", restored.Version, "bob")
	require.ErrorIs(t, err, admin.ErrBackupNotFound)
	_, err = fx.store.Restore(saved.Backup, "stale", "bob")
	require.ErrorIs(t, err, admin.ErrConflict)
}

func TestConfigStoreRestoreRefusesInvalidBackup(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	name := filepath.Base(fx.path) + ".bak.20260101T000000Z"
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(fx.path), name), []byte("[sever]\n"), 0600))

	_, err := fx.store.Restore(name, fx.load(t).Version, "bob")
	require.ErrorIs(t, err, admin.ErrInvalid)
	data, _ := os.ReadFile(fx.path)
	assert.Equal(t, storeBaseConfig, string(data))
}

func TestConfigStoreKeepsFormat(t *testing.T) {
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")
	path := writeConfigFile(t, "config.yaml", "server:\n  port: 8080\nfeeds:\n  show:\n    url: https://rumble.com/c/show\n")
	store := newFileConfigStore(path, testSchema(), nil, nil)
	store.environ = func() []string { return nil }

	snapshot, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, "yaml", snapshot.Format)
	snapshot.Document["server"].(map[string]interface{})["port"] = int64(9000)
	_, err = store.Save(snapshot.Document, snapshot.Version, "alice")
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "port: 9000", "a YAML file stays YAML")
}

func TestWriteFileReplacingFallsBackToInPlace(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires POSIX permissions enforced for a non-root user")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0640))
	// A read-only directory prevents the temp file and rename, like a single-file bind mount.
	require.NoError(t, os.Chmod(dir, 0500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	require.NoError(t, writeFileReplacing(path, []byte("new content")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "new content", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0640), info.Mode().Perm(), "permissions are kept")
}

func TestReloaderSkipsUnchangedContentAndTracksRestart(t *testing.T) {
	fx := newStoreFixture(t, storeBaseConfig)
	reloader := fx.store.reloader

	changes, err := reloader.Reload("test")
	require.NoError(t, err)
	assert.True(t, changes.empty())
	assert.Zero(t, fx.updater.calls, "unchanged content is not applied again")

	require.NoError(t, os.WriteFile(fx.path, []byte(strings.Replace(storeBaseConfig, "port = 8080", "port = 9000", 1)), 0644))
	_, err = reloader.Reload("test")
	require.NoError(t, err)
	assert.Equal(t, []string{"server"}, reloader.PendingRestart())

	// Another reload does not hide the pending restart.
	require.NoError(t, os.WriteFile(fx.path, []byte(strings.Replace(storeBaseConfig, "port = 8080", "port = 9000", 1)+"\n[cleanup]\nkeep_last = 5\n"), 0644))
	_, err = reloader.Reload("test")
	require.NoError(t, err)
	assert.Equal(t, []string{"server"}, reloader.PendingRestart())
}
