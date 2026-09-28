//go:build !unix

package audiobookshelf

import "os"

// fileIdentity is unavailable on this platform; the exporter falls back to
// os.Link errors for cross-device detection and os.SameFile for verification.
func fileIdentity(_ os.FileInfo) (fileID, bool) {
	return fileID{}, false
}
