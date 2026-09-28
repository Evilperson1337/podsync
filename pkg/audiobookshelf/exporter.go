package audiobookshelf

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pkg/errors"

	"github.com/mxpv/podsync/pkg/model"
)

// Status describes the outcome of an export, removal or reconciliation.
type Status string

const (
	// StatusLinked means a new hardlink was created.
	StatusLinked = Status("linked")
	// StatusAlreadyLinked means the destination already is the same inode as the source.
	StatusAlreadyLinked = Status("already_linked")
	// StatusCrossDevice means source and destination live on different filesystems.
	StatusCrossDevice = Status("cross_device")
	// StatusConflict means the destination exists and is a different file.
	StatusConflict = Status("conflict")
	// StatusFailed means the operation failed for another reason (missing root, permissions, ...).
	StatusFailed = Status("failed")
	// StatusRemoved means the Audiobookshelf hardlink was deleted.
	StatusRemoved = Status("removed")
	// StatusAbsent means there is no Audiobookshelf file at the destination.
	StatusAbsent = Status("absent")
	// StatusUnverified means the source is gone and the destination cannot be proven to be Podsync's link.
	StatusUnverified = Status("unverified")
	// StatusDeletedInLibrary means the recorded hardlink was deleted from Audiobookshelf and the
	// Podsync file has no other links, so the Podsync copy should be removed to mirror the library.
	StatusDeletedInLibrary = Status("deleted_in_library")
	// StatusLinkElsewhere means the recorded hardlink is missing from its path but the source still has
	// other links (moved, renamed, or library not mounted); nothing is changed.
	StatusLinkElsewhere = Status("link_elsewhere")
)

var (
	// ErrConflict is returned when the destination exists and is not the source inode.
	ErrConflict = errors.New("destination already exists and is a different file")
	// ErrCrossDevice is returned when a hardlink is impossible because source and destination are on different devices.
	ErrCrossDevice = errors.New("source and destination are on different devices; hardlink is not possible")
	// ErrUnverified is returned when a destination cannot be proven to be Podsync's hardlink.
	ErrUnverified = errors.New("destination cannot be verified as Podsync's hardlink")
)

// Result reports what an operation did.
type Result struct {
	Status      Status
	Source      string
	Destination string
	// SourceMissing is set when the Podsync source file does not exist.
	SourceMissing bool
	// SourceDevice and DestinationDevice are populated where the platform exposes them.
	SourceDevice      uint64
	DestinationDevice uint64
	// Inode and Links describe the linked file, where available.
	Inode uint64
	Links uint64
	// Record identifies a verified hardlink at Destination. It is set for linked and already_linked
	// results on platforms that expose device/inode numbers, and should be persisted by the caller.
	Record *model.HardlinkRecord
}

// Exporter hardlinks finalized episode media into an Audiobookshelf podcast library.
type Exporter struct {
	podcastRoot string
	// identify extracts device/inode information; overridable in tests.
	identify func(os.FileInfo) (fileID, bool)
}

type fileID struct {
	dev   uint64
	ino   uint64
	nlink uint64
}

// NewExporter creates an exporter rooted at the Audiobookshelf podcast library directory.
func NewExporter(podcastRoot string) *Exporter {
	return &Exporter{podcastRoot: podcastRoot, identify: fileIdentity}
}

// PodcastRoot returns the configured podcast library root.
func (e *Exporter) PodcastRoot() string {
	return e.podcastRoot
}

type paths struct {
	source  string
	root    string
	destDir string
}

// Export hardlinks sourcePath into <podcast_root>/<directory>/<basename of sourcePath>.
//
// It never copies bytes and never overwrites an existing destination. An existing
// destination that is already the same inode as the source is treated as success.
func (e *Exporter) Export(sourcePath string, directory string) (Result, error) {
	p, result, err := e.prepare(sourcePath, directory)
	if err != nil {
		return result, err
	}
	sourceInfo, err := os.Stat(p.source)
	if err != nil {
		result.SourceMissing = os.IsNotExist(err)
		return result, errors.Wrapf(err, "source media %q is not accessible", p.source)
	}
	return e.export(p, result, sourceInfo)
}

// Remove deletes the Audiobookshelf hardlink for sourcePath so the library mirrors Podsync.
//
// The destination is only removed when it is verifiably Podsync's hardlink: the same inode as the
// source, or, when the source is already gone, the device/inode in recorded. Unrelated files are
// never touched. A missing destination is reported as StatusAbsent without error.
func (e *Exporter) Remove(sourcePath string, directory string, recorded *model.HardlinkRecord) (Result, error) {
	p, result, err := e.prepare(sourcePath, directory)
	if err != nil {
		return result, err
	}

	sourceInfo, sourceErr := os.Stat(p.source)
	if sourceErr != nil && !os.IsNotExist(sourceErr) {
		return result, errors.Wrapf(sourceErr, "source media %q is not accessible", p.source)
	}
	result.SourceMissing = sourceErr != nil

	destInfo, err := os.Lstat(result.Destination)
	if os.IsNotExist(err) {
		result.Status = StatusAbsent
		return result, nil
	}
	if err != nil {
		return result, errors.Wrapf(err, "failed to stat destination %q", result.Destination)
	}
	if err := checkContained(p.root, p.destDir); err != nil {
		return result, err
	}

	if !destInfo.Mode().IsRegular() {
		result.Status = StatusConflict
		return result, errors.Wrapf(ErrConflict, "%q is not a regular file; refusing to delete it", result.Destination)
	}
	if result.SourceMissing {
		if !e.matchesRecord(result.Destination, destInfo, recorded) {
			result.Status = StatusUnverified
			return result, errors.Wrapf(ErrUnverified, "source %q is gone and %q does not match a recorded hardlink; leaving it in place", p.source, result.Destination)
		}
	} else if !os.SameFile(sourceInfo, destInfo) {
		result.Status = StatusConflict
		return result, errors.Wrapf(ErrConflict, "%q is not a hardlink of %q; refusing to delete it", result.Destination, p.source)
	}
	if id, ok := e.identify(destInfo); ok {
		result.Inode = id.ino
		result.Links = id.nlink
	}

	if err := os.Remove(result.Destination); err != nil {
		return result, errors.Wrapf(err, "failed to remove %q", result.Destination)
	}
	result.Status = StatusRemoved
	return result, nil
}

// Reconcile brings the library and Podsync back in sync for one episode, given the hardlink
// recorded for it (nil when none was recorded):
//
//   - source and destination both present: verify the link (already_linked or conflict);
//   - source present, destination missing, link recorded and the source has no other links:
//     the file was deleted in Audiobookshelf (deleted_in_library), and the caller removes the Podsync copy;
//   - source present, destination missing otherwise: create the link (backfill or repair);
//   - source missing: remove the destination if it is the recorded link (see Remove).
//
// Reconcile itself never deletes Podsync files.
func (e *Exporter) Reconcile(sourcePath string, directory string, recorded *model.HardlinkRecord) (Result, error) {
	p, result, err := e.prepare(sourcePath, directory)
	if err != nil {
		return result, err
	}
	if recorded != nil && recorded.Path != result.Destination {
		// The feed directory changed since the link was recorded; the old record does not apply here.
		recorded = nil
	}

	sourceInfo, err := os.Stat(p.source)
	if os.IsNotExist(err) {
		return e.Remove(sourcePath, directory, recorded)
	}
	if err != nil {
		return result, errors.Wrapf(err, "source media %q is not accessible", p.source)
	}

	_, err = os.Lstat(result.Destination)
	if err != nil && !os.IsNotExist(err) {
		return result, errors.Wrapf(err, "failed to stat destination %q", result.Destination)
	}
	if err != nil && recorded != nil {
		id, ok := e.identify(sourceInfo)
		if ok && id.dev == recorded.Device && id.ino == recorded.Inode {
			result.SourceDevice = id.dev
			result.Inode = id.ino
			result.Links = id.nlink
			if id.nlink <= 1 {
				result.Status = StatusDeletedInLibrary
				return result, nil
			}
			result.Status = StatusLinkElsewhere
			return result, errors.Errorf("recorded hardlink %q is missing but %q still has %d links; it may have been moved or the library is not mounted", result.Destination, p.source, id.nlink)
		}
	}
	return e.export(p, result, sourceInfo)
}

// prepare resolves paths and checks that the library root is reachable. Every operation requires the
// root to exist, so a missing mount is never mistaken for deleted files.
func (e *Exporter) prepare(sourcePath string, directory string) (paths, Result, error) {
	result := Result{Status: StatusFailed}
	p, err := e.resolve(sourcePath, directory)
	if err != nil {
		return p, result, err
	}
	result.Source = p.source
	result.Destination = filepath.Join(p.destDir, filepath.Base(p.source))

	rootInfo, err := os.Stat(p.root)
	if err != nil {
		return p, result, errors.Wrapf(err, "podcast_root %q is not accessible", p.root)
	}
	if !rootInfo.IsDir() {
		return p, result, errors.Errorf("podcast_root %q is not a directory", p.root)
	}
	return p, result, nil
}

func (e *Exporter) export(p paths, result Result, sourceInfo os.FileInfo) (Result, error) {
	if !sourceInfo.Mode().IsRegular() {
		return result, errors.Errorf("source media %q is not a regular file", p.source)
	}

	if err := os.MkdirAll(p.destDir, 0755); err != nil {
		return result, errors.Wrapf(err, "failed to create destination directory %q", p.destDir)
	}
	if err := checkContained(p.root, p.destDir); err != nil {
		return result, err
	}

	destDirInfo, err := os.Stat(p.destDir)
	if err != nil {
		return result, errors.Wrapf(err, "failed to stat destination directory %q", p.destDir)
	}

	sourceID, sourceOK := e.identify(sourceInfo)
	destDirID, destOK := e.identify(destDirInfo)
	if sourceOK {
		result.SourceDevice = sourceID.dev
		result.Inode = sourceID.ino
		result.Links = sourceID.nlink
	}
	if destOK {
		result.DestinationDevice = destDirID.dev
	}
	if sourceOK && destOK && sourceID.dev != destDirID.dev {
		result.Status = StatusCrossDevice
		return result, errors.Wrapf(ErrCrossDevice, "source device %d, destination device %d", sourceID.dev, destDirID.dev)
	}

	if err := os.Link(p.source, result.Destination); err != nil {
		switch {
		case os.IsExist(err):
			return e.checkExisting(result, sourceInfo)
		case errors.Is(err, syscall.EXDEV):
			result.Status = StatusCrossDevice
			return result, errors.Wrap(ErrCrossDevice, err.Error())
		default:
			return result, errors.Wrapf(err, "failed to hardlink %q to %q", p.source, result.Destination)
		}
	}

	destInfo, err := os.Lstat(result.Destination)
	if err != nil {
		return result, errors.Wrapf(err, "failed to verify hardlink %q", result.Destination)
	}
	if !os.SameFile(sourceInfo, destInfo) {
		return result, errors.Errorf("hardlink verification failed: %q and %q are different files", p.source, result.Destination)
	}
	result.Status = StatusLinked
	e.recordLink(&result, destInfo)
	return result, nil
}

func (e *Exporter) checkExisting(result Result, sourceInfo os.FileInfo) (Result, error) {
	destInfo, err := os.Lstat(result.Destination)
	if err != nil {
		return result, errors.Wrapf(err, "failed to stat existing destination %q", result.Destination)
	}
	if destInfo.Mode().IsRegular() && os.SameFile(sourceInfo, destInfo) {
		result.Status = StatusAlreadyLinked
		e.recordLink(&result, destInfo)
		return result, nil
	}
	result.Status = StatusConflict
	return result, errors.Wrapf(ErrConflict, "%q", result.Destination)
}

func (e *Exporter) recordLink(result *Result, destInfo os.FileInfo) {
	id, ok := e.identify(destInfo)
	if !ok {
		return
	}
	result.Inode = id.ino
	result.Links = id.nlink
	result.Record = &model.HardlinkRecord{Path: result.Destination, Device: id.dev, Inode: id.ino}
}

func (e *Exporter) matchesRecord(path string, info os.FileInfo, recorded *model.HardlinkRecord) bool {
	if recorded == nil || recorded.Path != path {
		return false
	}
	id, ok := e.identify(info)
	return ok && id.dev == recorded.Device && id.ino == recorded.Inode
}

// resolve validates the feed directory and returns absolute source, root and destination directory paths.
func (e *Exporter) resolve(sourcePath string, directory string) (paths, error) {
	if err := ValidateDirectory(directory); err != nil {
		return paths{}, err
	}
	source, err := filepath.Abs(filepath.Clean(sourcePath))
	if err != nil {
		return paths{}, errors.Wrapf(err, "failed to resolve source path %q", sourcePath)
	}
	root, err := filepath.Abs(filepath.Clean(e.podcastRoot))
	if err != nil {
		return paths{}, errors.Wrapf(err, "failed to resolve podcast_root %q", e.podcastRoot)
	}
	destDir := filepath.Join(root, filepath.Clean(strings.TrimSpace(directory)))
	if !isStrictlyWithin(root, destDir) {
		return paths{}, errors.Errorf("destination directory %q escapes podcast_root %q", destDir, root)
	}
	return paths{source: source, root: root, destDir: destDir}, nil
}

// checkContained refuses destinations whose symlinks resolve outside the library root.
func checkContained(root, destDir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.Wrapf(err, "failed to resolve podcast_root %q", root)
	}
	realDestDir, err := filepath.EvalSymlinks(destDir)
	if err != nil {
		return errors.Wrapf(err, "failed to resolve destination directory %q", destDir)
	}
	if !isStrictlyWithin(realRoot, realDestDir) {
		return errors.Errorf("destination directory %q resolves to %q outside podcast_root %q", destDir, realDestDir, realRoot)
	}
	return nil
}

// isStrictlyWithin reports whether path is below root (and not root itself).
func isStrictlyWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
