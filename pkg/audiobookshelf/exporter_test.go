package audiobookshelf

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type exportFixture struct {
	source string
	root   string
}

func newExportFixture(t *testing.T) exportFixture {
	t.Helper()
	base := t.TempDir()
	source := filepath.Join(base, "podsync", "doctrine", "ep-1.mp3")
	root := filepath.Join(base, "media", "podcasts")
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0755))
	require.NoError(t, os.MkdirAll(root, 0755))
	require.NoError(t, os.WriteFile(source, []byte("episode audio"), 0644))
	return exportFixture{source: source, root: root}
}

func assertSameFile(t *testing.T, a, b string) {
	t.Helper()
	aInfo, err := os.Stat(a)
	require.NoError(t, err)
	bInfo, err := os.Stat(b)
	require.NoError(t, err)
	assert.True(t, os.SameFile(aInfo, bInfo), "%s and %s should be the same inode", a, b)
}

func TestExportCreatesHardlink(t *testing.T) {
	fx := newExportFixture(t)

	result, err := NewExporter(fx.root).Export(fx.source, "Doctrine")
	require.NoError(t, err)

	expected := filepath.Join(fx.root, "Doctrine", "ep-1.mp3")
	assert.Equal(t, StatusLinked, result.Status)
	assert.Equal(t, fx.source, result.Source)
	assert.Equal(t, expected, result.Destination)
	assertSameFile(t, fx.source, expected)

	if runtime.GOOS != "windows" {
		assert.EqualValues(t, 2, result.Links)
		assert.NotZero(t, result.Inode)
		assert.Equal(t, result.SourceDevice, result.DestinationDevice)
	}
}

func TestExportCreatesDestinationDirectory(t *testing.T) {
	fx := newExportFixture(t)

	_, err := NewExporter(fx.root).Export(fx.source, filepath.Join("Nested", "Doctrine"))
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(fx.root, "Nested", "Doctrine"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestExportIsIdempotent(t *testing.T) {
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)

	_, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)

	result, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)
	assert.Equal(t, StatusAlreadyLinked, result.Status)
	assertSameFile(t, fx.source, result.Destination)
}

func TestExportConflictDoesNotOverwrite(t *testing.T) {
	fx := newExportFixture(t)
	destination := filepath.Join(fx.root, "Doctrine", "ep-1.mp3")
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0755))
	require.NoError(t, os.WriteFile(destination, []byte("different"), 0644))

	result, err := NewExporter(fx.root).Export(fx.source, "Doctrine")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict))
	assert.Equal(t, StatusConflict, result.Status)

	data, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, "different", string(data))
}

func TestExportRejectsInvalidDirectories(t *testing.T) {
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)

	for _, dir := range []string{"", "  ", ".", "..", "../outside", "Doctrine/../../outside", "/abs/path"} {
		t.Run(dir, func(t *testing.T) {
			_, err := exporter.Export(fx.source, dir)
			assert.Error(t, err)
		})
	}

	entries, err := os.ReadDir(filepath.Dir(fx.root))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "nothing should be created next to podcast_root")
}

func TestExportRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on Windows")
	}
	fx := newExportFixture(t)
	outside := filepath.Join(filepath.Dir(fx.root), "outside")
	require.NoError(t, os.MkdirAll(outside, 0755))
	require.NoError(t, os.Symlink(outside, filepath.Join(fx.root, "Escape")))

	_, err := NewExporter(fx.root).Export(fx.source, "Escape")
	require.Error(t, err)

	_, err = os.Stat(filepath.Join(outside, "ep-1.mp3"))
	assert.True(t, os.IsNotExist(err))
}

func TestExportMissingSource(t *testing.T) {
	fx := newExportFixture(t)

	result, err := NewExporter(fx.root).Export(filepath.Join(filepath.Dir(fx.source), "missing.mp3"), "Doctrine")
	require.Error(t, err)
	assert.True(t, errors.Is(err, os.ErrNotExist))
	assert.Contains(t, err.Error(), "missing.mp3")
	assert.Equal(t, StatusFailed, result.Status)
}

func TestExportMissingPodcastRoot(t *testing.T) {
	fx := newExportFixture(t)
	root := filepath.Join(fx.root, "does-not-exist")

	_, err := NewExporter(root).Export(fx.source, "Doctrine")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "podcast_root")

	_, err = os.Stat(root)
	assert.True(t, os.IsNotExist(err), "podcast_root must not be created implicitly")
}

func TestExportCrossDeviceDoesNotCopy(t *testing.T) {
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	// Pretend every directory lives on a different device than regular files.
	exporter.identify = func(info os.FileInfo) (fileID, bool) {
		if info.IsDir() {
			return fileID{dev: 2, ino: 1}, true
		}
		return fileID{dev: 1, ino: 1}, true
	}

	result, err := exporter.Export(fx.source, "Doctrine")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrCrossDevice))
	assert.Equal(t, StatusCrossDevice, result.Status)
	assert.EqualValues(t, 1, result.SourceDevice)
	assert.EqualValues(t, 2, result.DestinationDevice)

	_, err = os.Stat(result.Destination)
	assert.True(t, os.IsNotExist(err), "cross-device export must not fall back to copying")
}

func TestValidateDirectory(t *testing.T) {
	valid := []string{"Doctrine", "Some Show", "Nested/Show", "./Doctrine", "Doctrine/../Other"}
	for _, dir := range valid {
		assert.NoError(t, ValidateDirectory(dir), dir)
	}

	invalid := []string{"", " ", ".", "..", "../x", "a/../../x", "/abs", `\abs`}
	for _, dir := range invalid {
		assert.Error(t, ValidateDirectory(dir), dir)
	}
}

func TestRemoveDeletesHardlink(t *testing.T) {
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)

	result, err := exporter.Remove(fx.source, "Doctrine", nil)
	require.NoError(t, err)
	assert.Equal(t, StatusRemoved, result.Status)

	_, err = os.Stat(exported.Destination)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(fx.source)
	assert.NoError(t, err, "Remove must not touch the Podsync source")
}

func TestRemoveAbsentIsNoop(t *testing.T) {
	fx := newExportFixture(t)

	result, err := NewExporter(fx.root).Remove(fx.source, "Doctrine", nil)
	require.NoError(t, err)
	assert.Equal(t, StatusAbsent, result.Status)
}

func TestRemoveRefusesUnrelatedFile(t *testing.T) {
	fx := newExportFixture(t)
	destination := filepath.Join(fx.root, "Doctrine", "ep-1.mp3")
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0755))
	require.NoError(t, os.WriteFile(destination, []byte("different"), 0644))

	result, err := NewExporter(fx.root).Remove(fx.source, "Doctrine", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrConflict))
	assert.Equal(t, StatusConflict, result.Status)

	data, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, "different", string(data))
}

func skipWithoutInodes(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("device/inode records are not available on Windows")
	}
}

func TestExportReturnsRecord(t *testing.T) {
	skipWithoutInodes(t)
	fx := newExportFixture(t)

	result, err := NewExporter(fx.root).Export(fx.source, "Doctrine")
	require.NoError(t, err)
	require.NotNil(t, result.Record)
	assert.Equal(t, result.Destination, result.Record.Path)
	assert.Equal(t, result.Inode, result.Record.Inode)
	assert.Equal(t, result.SourceDevice, result.Record.Device)
}

func TestRemoveWithoutSourceRequiresRecord(t *testing.T) {
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)
	require.NoError(t, os.Remove(fx.source))

	result, err := exporter.Remove(fx.source, "Doctrine", nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnverified))
	assert.Equal(t, StatusUnverified, result.Status)
	assert.True(t, result.SourceMissing)

	_, err = os.Stat(exported.Destination)
	assert.NoError(t, err, "an unverifiable destination must not be deleted")
}

func TestRemoveWithoutSourceUsesRecord(t *testing.T) {
	skipWithoutInodes(t)
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)
	require.NoError(t, os.Remove(fx.source))

	result, err := exporter.Remove(fx.source, "Doctrine", exported.Record)
	require.NoError(t, err)
	assert.Equal(t, StatusRemoved, result.Status)
	_, err = os.Stat(exported.Destination)
	assert.True(t, os.IsNotExist(err))
}

func TestRemoveWithoutSourceRejectsMismatchedRecord(t *testing.T) {
	skipWithoutInodes(t)
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)

	// Replace the library file with an unrelated file of the same name, then drop the source.
	require.NoError(t, os.Remove(exported.Destination))
	require.NoError(t, os.WriteFile(exported.Destination, []byte("unrelated"), 0644))
	require.NoError(t, os.Remove(fx.source))

	_, err = exporter.Remove(fx.source, "Doctrine", exported.Record)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnverified))
	data, err := os.ReadFile(exported.Destination)
	require.NoError(t, err)
	assert.Equal(t, "unrelated", string(data))
}

func TestRemoveFailsWhenRootMissing(t *testing.T) {
	fx := newExportFixture(t)

	result, err := NewExporter(filepath.Join(fx.root, "unmounted")).Remove(fx.source, "Doctrine", nil)
	require.Error(t, err, "a missing library must not be reported as an absent file")
	assert.Equal(t, StatusFailed, result.Status)
}

func TestReconcileLinksWithoutRecord(t *testing.T) {
	fx := newExportFixture(t)

	result, err := NewExporter(fx.root).Reconcile(fx.source, "Doctrine", nil)
	require.NoError(t, err)
	assert.Equal(t, StatusLinked, result.Status)
	assertSameFile(t, fx.source, result.Destination)
}

func TestReconcileDetectsDeletionInLibrary(t *testing.T) {
	skipWithoutInodes(t)
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)
	require.NoError(t, os.Remove(exported.Destination))

	result, err := exporter.Reconcile(fx.source, "Doctrine", exported.Record)
	require.NoError(t, err)
	assert.Equal(t, StatusDeletedInLibrary, result.Status)
	assert.EqualValues(t, 1, result.Links)

	_, err = os.Stat(fx.source)
	assert.NoError(t, err, "Reconcile must never delete the Podsync source itself")
	_, err = os.Stat(exported.Destination)
	assert.True(t, os.IsNotExist(err), "a deleted link must not be recreated")
}

func TestReconcileLinkElsewhereChangesNothing(t *testing.T) {
	skipWithoutInodes(t)
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)
	// Simulate a renamed podcast folder: the link still exists, just not at the recorded path.
	require.NoError(t, os.Rename(filepath.Dir(exported.Destination), filepath.Join(fx.root, "Renamed")))

	result, err := exporter.Reconcile(fx.source, "Doctrine", exported.Record)
	require.Error(t, err)
	assert.Equal(t, StatusLinkElsewhere, result.Status)

	_, err = os.Stat(exported.Destination)
	assert.True(t, os.IsNotExist(err), "no duplicate link should be created")
	_, err = os.Stat(fx.source)
	assert.NoError(t, err)
}

func TestReconcileIgnoresRecordForOtherPath(t *testing.T) {
	skipWithoutInodes(t)
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Old")
	require.NoError(t, err)
	require.NoError(t, os.Remove(exported.Destination))

	result, err := exporter.Reconcile(fx.source, "Doctrine", exported.Record)
	require.NoError(t, err)
	assert.Equal(t, StatusLinked, result.Status)
}

func TestReconcileRemovesRecordedLinkWhenSourceMissing(t *testing.T) {
	skipWithoutInodes(t)
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)
	require.NoError(t, os.Remove(fx.source))

	result, err := exporter.Reconcile(fx.source, "Doctrine", exported.Record)
	require.NoError(t, err)
	assert.Equal(t, StatusRemoved, result.Status)
	assert.True(t, result.SourceMissing)
	_, err = os.Stat(exported.Destination)
	assert.True(t, os.IsNotExist(err))
}

func TestReconcileFailsWhenRootMissing(t *testing.T) {
	skipWithoutInodes(t)
	fx := newExportFixture(t)
	exporter := NewExporter(fx.root)
	exported, err := exporter.Export(fx.source, "Doctrine")
	require.NoError(t, err)
	require.NoError(t, os.Remove(exported.Destination))

	_, err = NewExporter(filepath.Join(fx.root, "unmounted")).Reconcile(fx.source, "Doctrine", exported.Record)
	require.Error(t, err)
}
