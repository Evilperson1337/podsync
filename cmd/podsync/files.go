package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/services/admin"
	"github.com/mxpv/podsync/services/update"
)

var (
	// safeDirName matches feed IDs usable as a directory name (no path separators, no leading dot).
	safeDirName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
	// safeFileName matches uploaded file names.
	safeFileName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9 ._()-]*$`)

	signatureExtensions = map[string]bool{".wav": true, ".mp3": true, ".m4a": true, ".flac": true, ".ogg": true, ".opus": true, ".aac": true}
)

// maxDirectoryListing bounds directory listings returned to the editor.
const maxDirectoryListing = 2000

// configFiles gives the admin editor access to files referenced by the running configuration.
type configFiles struct {
	reloader *configReloader
	// probe checks that an uploaded file is readable audio; nil skips the check.
	probe func(path string) error
}

func (f configFiles) AudiobookshelfDirectories() (admin.DirectoryListing, error) {
	root := strings.TrimSpace(f.reloader.Current().Audiobookshelf.PodcastRoot)
	if root == "" {
		return admin.DirectoryListing{}, errors.Wrap(admin.ErrNotConfigured, "set audiobookshelf.podcast_root and save to browse podcast directories")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return admin.DirectoryListing{}, errors.Wrapf(err, "podcast_root %q is not accessible", root)
	}
	listing := admin.DirectoryListing{Root: root, Directories: []string{}}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Follow symlinks so linked podcast folders are listed too.
		if info, err := os.Stat(filepath.Join(root, name)); err != nil || !info.IsDir() {
			continue
		}
		listing.Directories = append(listing.Directories, name)
		if len(listing.Directories) >= maxDirectoryListing {
			break
		}
	}
	sort.Strings(listing.Directories)
	return listing, nil
}

func (f configFiles) signatureDir(feedID string) (string, error) {
	if !safeDirName.MatchString(feedID) {
		return "", errors.Wrapf(admin.ErrInvalidFile, "feed ID %q cannot be used as a directory name", feedID)
	}
	root := f.reloader.Current().signaturesRoot()
	if root == "" {
		return "", errors.Wrap(admin.ErrNotConfigured, "set [signatures] root_dir (or use local storage) to manage signature files")
	}
	return update.SignaturesDir(root, feedID), nil
}

func (f configFiles) SignatureFiles(feedID string) (admin.SignatureListing, error) {
	dir, err := f.signatureDir(feedID)
	if err != nil {
		return admin.SignatureListing{}, err
	}
	listing := admin.SignatureListing{Directory: dir, Files: []admin.SignatureFile{}}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return listing, nil
	}
	if err != nil {
		return admin.SignatureListing{}, errors.Wrapf(err, "signatures directory %q is not accessible", dir)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !signatureExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		listing.Files = append(listing.Files, admin.SignatureFile{Name: entry.Name(), Size: info.Size(), ModifiedAt: info.ModTime().UTC()})
	}
	sort.Slice(listing.Files, func(i, j int) bool { return listing.Files[i].Name < listing.Files[j].Name })
	return listing, nil
}

func (f configFiles) SaveSignatureFile(feedID, name string, content io.Reader, replace bool) (admin.SignatureFile, error) {
	dir, err := f.signatureDir(feedID)
	if err != nil {
		return admin.SignatureFile{}, err
	}
	// Browsers may send a full client path; keep only the file name.
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if !safeFileName.MatchString(name) {
		return admin.SignatureFile{}, errors.Wrapf(admin.ErrInvalidFile, "file name %q may only contain letters, digits, spaces and . _ - ( )", name)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !signatureExtensions[ext] {
		return admin.SignatureFile{}, errors.Wrapf(admin.ErrInvalidFile, "%q is not a supported audio file (wav, mp3, m4a, flac, ogg, opus, aac)", name)
	}
	target := filepath.Join(dir, name)
	if _, err := os.Stat(target); err == nil && !replace {
		return admin.SignatureFile{}, errors.Wrapf(admin.ErrFileExists, "%q", name)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return admin.SignatureFile{}, errors.Wrap(err, "failed to create the signatures directory")
	}

	tmp, err := os.CreateTemp(dir, ".upload-*"+ext)
	if err != nil {
		return admin.SignatureFile{}, errors.Wrap(err, "failed to store the upload")
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	written, err := io.Copy(tmp, io.LimitReader(content, admin.MaxSignatureUpload+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return admin.SignatureFile{}, errors.Wrap(err, "failed to store the upload")
	}
	switch {
	case written == 0:
		return admin.SignatureFile{}, errors.Wrap(admin.ErrInvalidFile, "the file is empty")
	case written > admin.MaxSignatureUpload:
		return admin.SignatureFile{}, errors.Wrapf(admin.ErrInvalidFile, "signature files are limited to %d MB", admin.MaxSignatureUpload>>20)
	}
	if f.probe != nil {
		if err := f.probe(tmpName); err != nil {
			return admin.SignatureFile{}, errors.Wrapf(admin.ErrInvalidFile, "%q is not readable audio: %v", name, err)
		}
	}
	if err := os.Chmod(tmpName, 0644); err != nil {
		return admin.SignatureFile{}, errors.Wrap(err, "failed to store the upload")
	}
	if err := os.Rename(tmpName, target); err != nil {
		return admin.SignatureFile{}, errors.Wrap(err, "failed to store the upload")
	}
	keep = true

	info, err := os.Stat(target)
	if err != nil {
		return admin.SignatureFile{}, errors.Wrap(err, "failed to store the upload")
	}
	return admin.SignatureFile{Name: name, Size: info.Size(), ModifiedAt: info.ModTime().UTC()}, nil
}

// probeAudioFile checks with ffprobe that a file has a readable audio stream. It is skipped when
// ffprobe is not installed; signature trimming validates ffmpeg availability separately.
func probeAudioFile(path string) error {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_type", "-of", "csv=p=0", path).CombinedOutput()
	if err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	if !strings.Contains(string(output), "audio") {
		return errors.New("no audio stream found")
	}
	return nil
}
