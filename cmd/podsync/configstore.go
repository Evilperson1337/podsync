package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/pelletier/go-toml"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/configschema"
	"github.com/mxpv/podsync/services/admin"
)

// defaultConfigBackups is how many previous configuration versions the admin interface keeps.
const defaultConfigBackups = 10

// backupTimeFormat is the timestamp in backup names: <config file>.bak.<UTC time>.
const backupTimeFormat = "20060102T150405Z"

// fileConfigStore lets the admin interface read, validate and write the configuration file.
type fileConfigStore struct {
	path            string
	schema          *configschema.Schema
	reloader        *configReloader
	validateRuntime func(*Config) error
	maxBackups      int
	now             func() time.Time
	environ         func() []string

	mu sync.Mutex // serializes saves and restores
}

func newFileConfigStore(path string, schema *configschema.Schema, reloader *configReloader, validateRuntime func(*Config) error) *fileConfigStore {
	return &fileConfigStore{
		path:            path,
		schema:          schema,
		reloader:        reloader,
		validateRuntime: validateRuntime,
		maxBackups:      defaultConfigBackups,
		now:             time.Now,
		environ:         os.Environ,
	}
}

func (s *fileConfigStore) format() configFormat {
	return configFormatFor(s.path)
}

// Load returns the configuration file as an editable document.
func (s *fileConfigStore) Load() (admin.ConfigSnapshot, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return admin.ConfigSnapshot{}, errors.Wrap(err, "failed to read the configuration file")
	}
	tree, err := parseConfigTree(s.format(), data)
	if err != nil {
		return admin.ConfigSnapshot{}, errors.Wrap(err, "the configuration file cannot be parsed; fix it by hand or restore a backup")
	}
	snapshot := admin.ConfigSnapshot{
		Path:         absPath(s.path),
		Format:       string(s.format()),
		Version:      configContentHash(data),
		Document:     tree.ToMap(),
		EnvOverrides: configEnvOverrides(tree, s.environ()),
	}
	if s.reloader != nil {
		snapshot.PendingRestart = s.reloader.PendingRestart()
	}
	return snapshot, nil
}

// Validate checks a document as if it were written to the configuration file.
func (s *fileConfigStore) Validate(document map[string]interface{}) (admin.Validation, error) {
	_, validation := s.check(document)
	return validation, nil
}

// check renders a document and validates the result through the normal loader.
func (s *fileConfigStore) check(document map[string]interface{}) ([]byte, admin.Validation) {
	normalized, err := normalizeConfigValue(document, "")
	if err != nil {
		return nil, admin.Validation{Errors: []admin.ValidationIssue{admin.Issue(err.Error())}}
	}
	values, _ := normalized.(map[string]interface{})
	if values == nil {
		values = map[string]interface{}{}
	}
	rendered, err := renderConfig(s.format(), values, s.schema)
	if err != nil {
		return nil, admin.Validation{Errors: []admin.ValidationIssue{admin.Issue(err.Error())}}
	}
	return rendered, s.checkContent(rendered)
}

// checkContent validates configuration content with the loader and runtime checks.
func (s *fileConfigStore) checkContent(data []byte) admin.Validation {
	validation := admin.Validation{Preview: string(data)}
	cfg, err := loadConfigData(s.path, data)
	if err != nil {
		validation.Errors = splitConfigErrors(err)
		return validation
	}
	if s.validateRuntime != nil {
		if err := s.validateRuntime(cfg); err != nil {
			validation.Errors = []admin.ValidationIssue{admin.Issue(err.Error())}
			return validation
		}
	}
	validation.Valid = true
	if s.reloader != nil {
		s.reloader.mu.Lock()
		validation.RestartRequired = restartOnlyChanges(s.reloader.startupConfig(), cfg)
		s.reloader.mu.Unlock()
	}
	return validation
}

// splitConfigErrors turns a validation error into one issue per problem, keeping the option path
// of each where the validator attached one.
func splitConfigErrors(err error) []admin.ValidationIssue {
	var multi *multierror.Error
	if errors.As(err, &multi) && len(multi.Errors) > 0 {
		issues := make([]admin.ValidationIssue, 0, len(multi.Errors))
		for _, item := range multi.Errors {
			issues = append(issues, admin.Issue(item.Error(), configschema.ErrorPath(item)...))
		}
		return issues
	}
	return []admin.ValidationIssue{admin.Issue(err.Error(), configschema.ErrorPath(err)...)}
}

// Save writes a document to the configuration file if the file is still at version.
func (s *fileConfigStore) Save(document map[string]interface{}, version string, user string) (admin.SaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.readCurrent(version)
	if err != nil {
		return admin.SaveResult{}, err
	}
	rendered, validation := s.check(document)
	if !validation.Valid {
		return admin.SaveResult{Validation: validation}, admin.ErrInvalid
	}
	return s.write(current, rendered, validation, user, "saved")
}

// Restore writes a backup's exact content back to the configuration file.
func (s *fileConfigStore) Restore(name string, version string, user string) (admin.SaveResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	backups, err := s.listBackups()
	if err != nil {
		return admin.SaveResult{}, err
	}
	found := false
	for _, backup := range backups {
		if backup.Name == name {
			found = true
			break
		}
	}
	if !found {
		return admin.SaveResult{}, errors.Wrapf(admin.ErrBackupNotFound, "%q", name)
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(s.path), name))
	if err != nil {
		return admin.SaveResult{}, errors.Wrap(err, "failed to read backup")
	}

	current, err := s.readCurrent(version)
	if err != nil {
		return admin.SaveResult{}, err
	}
	validation := s.checkContent(data)
	if !validation.Valid {
		return admin.SaveResult{Validation: validation}, admin.ErrInvalid
	}
	return s.write(current, data, validation, user, "restored "+name)
}

// readCurrent reads the file and checks that it has not changed since version was loaded.
func (s *fileConfigStore) readCurrent(version string) ([]byte, error) {
	current, err := os.ReadFile(s.path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read the configuration file")
	}
	if version == "" || configContentHash(current) != version {
		return nil, admin.ErrConflict
	}
	return current, nil
}

func (s *fileConfigStore) write(previous, next []byte, validation admin.Validation, user, action string) (admin.SaveResult, error) {
	result := admin.SaveResult{Version: configContentHash(next), SavedAt: s.now().UTC(), Validation: validation}
	if bytes.Equal(previous, next) {
		return result, nil
	}

	backup, err := s.backup(previous)
	if err != nil {
		return admin.SaveResult{}, err
	}
	result.Backup = backup
	if err := writeFileReplacing(s.path, next); err != nil {
		return admin.SaveResult{}, err
	}

	logger := log.WithFields(log.Fields{"user": user, "config": s.path, "backup": backup})
	logger.Infof("configuration %s via the admin interface", action)

	if s.reloader != nil {
		changes, reloadErr := s.reloader.Reload("admin: " + action + " by " + user)
		result.FeedsAdded = changes.Added
		result.FeedsUpdated = changes.Updated
		result.FeedsRemoved = changes.Removed
		result.PendingRestart = s.reloader.PendingRestart()
		if reloadErr != nil {
			result.ReloadError = reloadErr.Error()
		}
	}
	return result, nil
}

// backup copies the current content next to the configuration file and prunes old backups.
func (s *fileConfigStore) backup(content []byte) (string, error) {
	dir, base := filepath.Dir(s.path), filepath.Base(s.path)
	stamp := s.now().UTC().Format(backupTimeFormat)
	name := base + ".bak." + stamp
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			break
		}
		name = base + ".bak." + stamp + "-" + strconv.Itoa(i)
	}
	// Backups hold API tokens and the admin password hash, so keep them private.
	if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
		return "", errors.Wrap(err, "failed to back up the configuration file; nothing was changed")
	}

	backups, err := s.listBackups()
	if err == nil && len(backups) > s.maxBackups {
		for _, old := range backups[s.maxBackups:] {
			if err := os.Remove(filepath.Join(dir, old.Name)); err != nil {
				log.WithError(err).WithField("backup", old.Name).Warn("failed to remove an old configuration backup")
			}
		}
	}
	return name, nil
}

// Backups lists previous configuration versions, newest first.
func (s *fileConfigStore) Backups() ([]admin.Backup, error) {
	return s.listBackups()
}

func (s *fileConfigStore) listBackups() ([]admin.Backup, error) {
	dir, base := filepath.Dir(s.path), filepath.Base(s.path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list configuration backups")
	}
	prefix := base + ".bak."
	var backups []admin.Backup
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) {
			continue
		}
		stamp, _, _ := strings.Cut(strings.TrimPrefix(name, prefix), "-")
		created, err := time.Parse(backupTimeFormat, stamp)
		if err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		backups = append(backups, admin.Backup{Name: name, CreatedAt: created, Size: info.Size()})
	}
	sort.Slice(backups, func(i, j int) bool {
		if !backups[i].CreatedAt.Equal(backups[j].CreatedAt) {
			return backups[i].CreatedAt.After(backups[j].CreatedAt)
		}
		return backups[i].Name > backups[j].Name
	})
	return backups, nil
}

// writeFileReplacing replaces path with data atomically where possible, keeping its permissions.
// Renaming over a file fails when that file is itself a bind mount (a Docker single-file volume);
// the file is then rewritten in place.
func writeFileReplacing(path string, data []byte) error {
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	dir, base := filepath.Dir(path), filepath.Base(path)
	tmp, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err == nil {
		tmpName := tmp.Name()
		_, err = tmp.Write(data)
		if err == nil {
			err = tmp.Sync()
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Chmod(tmpName, mode)
		}
		if err == nil {
			if err = os.Rename(tmpName, path); err == nil {
				return nil
			}
		}
		_ = os.Remove(tmpName)
	}

	log.WithError(err).WithField("config", path).Warn("could not replace the configuration file atomically; rewriting it in place (mount the directory rather than the single file to avoid this)")
	file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, mode)
	if openErr != nil {
		return errors.Wrap(openErr, "failed to write the configuration file")
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return errors.Wrap(err, "failed to write the configuration file")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.Wrap(err, "failed to write the configuration file")
	}
	return file.Close()
}

// configEnvOverrides lists options that environment variables override: PODSYNC__ keys and the
// PODSYNC_<PROVIDER>_API_KEY token variables.
func configEnvOverrides(tree *toml.Tree, environ []string) []admin.EnvOverride {
	overrides := []admin.EnvOverride{}
	target := reflect.TypeOf(Config{})
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(name), configEnvPrefix) {
			keys, _, err := resolveConfigEnvPath(tree, target, strings.Split(name[len(configEnvPrefix):], "__"))
			if err == nil {
				overrides = append(overrides, admin.EnvOverride{Path: keys, Variable: name})
			}
			continue
		}
		for provider, variable := range legacyAPIKeyEnv {
			if name == variable && value != "" {
				overrides = append(overrides, admin.EnvOverride{Path: []string{"tokens", string(provider)}, Variable: name})
			}
		}
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Variable < overrides[j].Variable })
	return overrides
}
