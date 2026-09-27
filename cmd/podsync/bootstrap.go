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

//go:embed starter_config.yaml
var starterConfigYAML []byte

// starterConfigJSON is the JSON starter. JSON has no comments, so it is kept minimal; see
// config.toml.example for every option (JSON uses the same keys and nesting).
var starterConfigJSON = []byte(`{
  "server": {
    "port": 8080
  },
  "storage": {
    "type": "local"
  },
  "tokens": {},
  "feeds": {}
}
`)

// starterConfigFor returns the starter configuration in the format matching path.
func starterConfigFor(path string) []byte {
	switch configFormatFor(path) {
	case configFormatYAML:
		return starterConfigYAML
	case configFormatJSON:
		return starterConfigJSON
	default:
		return starterConfig
	}
}

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
	if _, err := file.Write(starterConfigFor(path)); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return errors.Wrapf(err, "failed to write %s", path)
	}
	return file.Close()
}

// defaultConfigPath is the --config default.
const defaultConfigPath = "config.toml"

// resolveConfigPath lets YAML and JSON users rely on the default path: when path is the default
// config.toml and it does not exist, the first of config.yaml, config.yml or config.json found in
// the same directory is used instead. Any other path is returned unchanged.
func resolveConfigPath(path string) string {
	if filepath.Base(path) != defaultConfigPath {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	dir := filepath.Dir(path)
	for _, name := range []string{"config.yaml", "config.yml", "config.json"} {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return path
}

// missingConfigHelp explains how to provide a configuration file.
func missingConfigHelp(path string) string {
	return fmt.Sprintf(`no configuration file found at %s

To get started, either:
  - create a starter configuration:  podsync --init --config %s
  - point Podsync at an existing file: --config /path/to/config.toml (or PODSYNC_CONFIG_PATH)
  - in Docker, mount your file:        -v /path/to/config.toml:/app/config.toml

Configuration files can be TOML, YAML (.yaml/.yml) or JSON (.json).`, path, path)
}

// absPath returns an absolute form of path for messages, falling back to path itself.
func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
