//go:build unix

package audiobookshelf

import (
	"os"
	"syscall"
)

func fileIdentity(info os.FileInfo) (fileID, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fileID{}, false
	}
	// Field widths differ between platforms, so conversions are required on some of them.
	return fileID{
		dev:   uint64(st.Dev),   //nolint:unconvert
		ino:   uint64(st.Ino),   //nolint:unconvert
		nlink: uint64(st.Nlink), //nolint:unconvert
	}, true
}
