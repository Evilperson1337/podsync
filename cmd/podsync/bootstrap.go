package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
)

// starterConfig is written by --init and on first run when no configuration file exists.
//
//go:embed starter_config.toml
var starterConfig []byte

// writeStarterConfig creates a starter configuration at path. It never overwrites an existing
// file, and it does not create missing parent directories: a missing directory usually means a
// mistyped path or a volume that is not mounted.
func writeStarterConfig(path string) error {
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return errors.Wrapf(err, "directory %s is not accessible", dir)
	}
	if !info.IsDir() {
		return errors.Errorf("%s is not a directory", dir)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			return errors.Errorf("%s already exists; refusing to overwrite it", path)
		}
		return errors.Wrapf(err, "failed to create %s", path)
	}
	if _, err := file.Write(starterConfig); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return errors.Wrapf(err, "failed to write %s", path)
	}
	return file.Close()
}

// missingConfigHelp explains how to provide a configuration file.
func missingConfigHelp(path string) string {
	return fmt.Sprintf(`no configuration file found at %s

To get started, either:
  - create a starter configuration:  podsync --init --config %s
  - point Podsync at an existing file: --config /path/to/config.toml (or PODSYNC_CONFIG_PATH)
  - in Docker, mount your file:        -v /path/to/config.toml:/app/config.toml`, path, path)
}

// absPath returns an absolute form of path for messages, falling back to path itself.
func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
