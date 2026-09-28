package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mxpv/podsync/services/admin"
)

func newFilesFixture(t *testing.T, extra string) (configFiles, string) {
	t.Helper()
	t.Setenv("PODSYNC_SIGNATURES_DIR", "")
	reloader, _, _, path := newTestReloader(t, reloadBaseConfig+extra)
	return configFiles{reloader: reloader}, filepath.Dir(path)
}

func TestAudiobookshelfDirectories(t *testing.T) {
	files, _ := newFilesFixture(t, "")
	_, err := files.AudiobookshelfDirectories()
	require.ErrorIs(t, err, admin.ErrNotConfigured, "no podcast_root configured")

	root := t.TempDir()
	for _, dir := range []string{"Zeta Show", "Alpha", ".hidden"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0644))
	require.NoError(t, os.Symlink(filepath.Join(root, "Alpha"), filepath.Join(root, "Linked")))

	files, _ = newFilesFixture(t, "\n[audiobookshelf]\npodcast_root = \""+filepath.ToSlash(root)+"\"\n")
	listing, err := files.AudiobookshelfDirectories()
	require.NoError(t, err)
	assert.Equal(t, root, listing.Root)
	assert.Equal(t, []string{"Alpha", "Linked", "Zeta Show"}, listing.Directories, "sorted, hidden entries and files skipped, symlinked folders included")
}

func TestSignatureFilesUploadAndList(t *testing.T) {
	files, dir := newFilesFixture(t, "")
	sigDir := filepath.Join(dir, "data", "show", "signatures")

	listing, err := files.SignatureFiles("show")
	require.NoError(t, err)
	assert.Equal(t, sigDir, listing.Directory, "defaults to the local data directory")
	assert.Empty(t, listing.Files, "a missing directory lists as empty")

	saved, err := files.SaveSignatureFile("show", `C:\Users\me\Intro Clip.wav`, strings.NewReader("RIFF-data"), false)
	require.NoError(t, err)
	assert.Equal(t, "Intro Clip.wav", saved.Name, "client paths are reduced to the file name")
	data, err := os.ReadFile(filepath.Join(sigDir, "Intro Clip.wav"))
	require.NoError(t, err)
	assert.Equal(t, "RIFF-data", string(data))

	_, err = files.SaveSignatureFile("show", "Intro Clip.wav", strings.NewReader("other"), false)
	require.ErrorIs(t, err, admin.ErrFileExists)
	_, err = files.SaveSignatureFile("show", "Intro Clip.wav", strings.NewReader("replaced"), true)
	require.NoError(t, err)
	data, _ = os.ReadFile(filepath.Join(sigDir, "Intro Clip.wav"))
	assert.Equal(t, "replaced", string(data))

	require.NoError(t, os.WriteFile(filepath.Join(sigDir, "readme.txt"), []byte("x"), 0644))
	listing, err = files.SignatureFiles("show")
	require.NoError(t, err)
	require.Len(t, listing.Files, 1, "only audio files are listed")
	assert.Equal(t, "Intro Clip.wav", listing.Files[0].Name)

	entries, _ := os.ReadDir(sigDir)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".upload-"), "no temporary files are left behind")
	}
}

func TestSignatureUploadRejections(t *testing.T) {
	files, dir := newFilesFixture(t, "")
	cases := map[string]struct {
		feedID, name, content string
	}{
		"traversal feed ID": {"../etc", "a.wav", "x"},
		"dot feed ID":       {"..", "a.wav", "x"},
		"not audio":         {"show", "script.sh", "x"},
		"hidden name":       {"show", ".a.wav", "x"},
		"odd characters":    {"show", "a;rm.wav", "x"},
		"empty file":        {"show", "empty.wav", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := files.SaveSignatureFile(tc.feedID, tc.name, strings.NewReader(tc.content), false)
			require.ErrorIs(t, err, admin.ErrInvalidFile)
		})
	}
	saved, err := files.SaveSignatureFile("show", "../../evil.wav", strings.NewReader("x"), false)
	require.NoError(t, err)
	assert.Equal(t, "evil.wav", saved.Name)
	_, err = os.Stat(filepath.Join(dir, "data", "show", "signatures", "evil.wav"))
	assert.NoError(t, err, "a path in the name is reduced to the file name inside the signatures directory")
	_, err = os.Stat(filepath.Join(dir, "evil.wav"))
	assert.True(t, os.IsNotExist(err), "nothing escapes the signatures directory")
}

func TestSignatureUploadProbe(t *testing.T) {
	files, dir := newFilesFixture(t, "")
	files.probe = func(string) error { return errors.New("invalid data found") }
	_, err := files.SaveSignatureFile("show", "fake.mp3", strings.NewReader("not audio"), false)
	require.ErrorIs(t, err, admin.ErrInvalidFile)
	assert.Contains(t, err.Error(), "not readable audio")
	_, err = os.Stat(filepath.Join(dir, "data", "show", "signatures", "fake.mp3"))
	assert.True(t, os.IsNotExist(err), "a rejected upload is not kept")
}

func TestProbeAudioFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}
	dir := t.TempDir()
	wav := filepath.Join(dir, "tone.wav")
	output, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "sine=duration=1", wav).CombinedOutput()
	require.NoError(t, err, string(output))
	assert.NoError(t, probeAudioFile(wav))

	fake := filepath.Join(dir, "fake.wav")
	require.NoError(t, os.WriteFile(fake, []byte("definitely not audio"), 0644))
	assert.Error(t, probeAudioFile(fake))
}
