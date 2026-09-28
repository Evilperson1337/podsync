package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// ConfigStore reads, validates and writes the configuration file. The podsync command implements
// it; it works on raw documents, and the admin server masks secrets on the way out.
type ConfigStore interface {
	Load() (ConfigSnapshot, error)
	Validate(document map[string]interface{}) (Validation, error)
	Save(document map[string]interface{}, version string, user string) (SaveResult, error)
	Backups() ([]Backup, error)
	Restore(name string, version string, user string) (SaveResult, error)
}

var (
	// ErrConflict means the configuration file changed since the editor loaded it.
	ErrConflict = errors.New("the configuration file was changed since it was loaded")
	// ErrInvalid means a submitted configuration failed validation; nothing was written.
	ErrInvalid = errors.New("the configuration is not valid")
	// ErrBackupNotFound means a restore named a backup that does not exist.
	ErrBackupNotFound = errors.New("backup not found")
)

// ConfigSnapshot is the configuration file as the editor sees it.
type ConfigSnapshot struct {
	Path    string `json:"path"`
	Format  string `json:"format"`
	Version string `json:"version"`
	// Document is the file content as nested values (before environment overrides and defaults).
	Document map[string]interface{} `json:"document"`
	// EnvOverrides lists options set by environment variables, which win over the file.
	EnvOverrides []EnvOverride `json:"env_overrides"`
	// PendingRestart lists sections whose saved changes take effect after a restart.
	PendingRestart []string `json:"pending_restart"`
}

// EnvOverride is an option set from the environment.
type EnvOverride struct {
	Path     []string `json:"path"`
	Variable string   `json:"variable"`
}

// Validation is the result of checking a candidate configuration.
type Validation struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors,omitempty"`
	// RestartRequired lists sections that would change but only take effect after a restart.
	RestartRequired []string `json:"restart_required,omitempty"`
	// Preview is the file that would be written.
	Preview string `json:"preview,omitempty"`
}

// SaveResult reports a successful save or restore.
type SaveResult struct {
	Version        string    `json:"version"`
	Backup         string    `json:"backup,omitempty"`
	SavedAt        time.Time `json:"saved_at"`
	FeedsAdded     []string  `json:"feeds_added,omitempty"`
	FeedsUpdated   []string  `json:"feeds_updated,omitempty"`
	FeedsRemoved   []string  `json:"feeds_removed,omitempty"`
	PendingRestart []string  `json:"pending_restart,omitempty"`
	// ReloadError is set when the file was written but applying it failed partially.
	ReloadError string     `json:"reload_error,omitempty"`
	Validation  Validation `json:"validation"`
}

// Backup is a previous version of the configuration file.
type Backup struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
}

// maxRequestBody bounds editor requests.
const maxRequestBody = 4 << 20

type documentRequest struct {
	Document map[string]interface{} `json:"document"`
	Version  string                 `json:"version"`
}

type restoreRequest struct {
	Version string `json:"version"`
}

type passwordRequest struct {
	Password string `json:"password"`
}

type errorResponse struct {
	Error      string          `json:"error"`
	Validation *Validation     `json:"validation,omitempty"`
	Current    *ConfigSnapshot `json:"current,omitempty"`
}

func (s *Server) registerConfigRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("POST /api/config/validate", s.handleValidateConfig)
	mux.HandleFunc("PUT /api/config", s.handleSaveConfig)
	mux.HandleFunc("GET /api/config/backups", s.handleBackups)
	mux.HandleFunc("POST /api/config/backups/{name}/restore", s.handleRestore)
	mux.HandleFunc("POST /api/password-hash", s.handlePasswordHash)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	snapshot, err := s.opts.Store.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err, nil, nil)
		return
	}
	masked := s.maskSnapshot(snapshot)
	writeJSON(w, masked)
}

func (s *Server) maskSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	snapshot.Document, _ = maskSecrets(snapshot.Document, s.opts.Schema).(map[string]interface{})
	return snapshot
}

// submittedDocument decodes a request and restores masked secrets from the current file.
func (s *Server) submittedDocument(w http.ResponseWriter, r *http.Request) (documentRequest, bool) {
	var request documentRequest
	if err := decodeRequest(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err, nil, nil)
		return request, false
	}
	if request.Document == nil {
		writeError(w, http.StatusBadRequest, errors.New("document is required"), nil, nil)
		return request, false
	}
	current, err := s.opts.Store.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err, nil, nil)
		return request, false
	}
	restored, err := restoreSecrets(request.Document, current.Document, nil)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err, nil, nil)
		return request, false
	}
	request.Document = restored.(map[string]interface{})
	return request, true
}

func (s *Server) handleValidateConfig(w http.ResponseWriter, r *http.Request) {
	request, ok := s.submittedDocument(w, r)
	if !ok {
		return
	}
	validation, err := s.opts.Store.Validate(request.Document)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err, nil, nil)
		return
	}
	writeJSON(w, validation)
}

func (s *Server) handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	request, ok := s.submittedDocument(w, r)
	if !ok {
		return
	}
	user := UserFromContext(r.Context())
	result, err := s.opts.Store.Save(request.Document, request.Version, user)
	s.writeSaveResult(w, result, err, "save")
}

func (s *Server) handleBackups(w http.ResponseWriter, _ *http.Request) {
	backups, err := s.opts.Store.Backups()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err, nil, nil)
		return
	}
	if backups == nil {
		backups = []Backup{}
	}
	writeJSON(w, backups)
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	var request restoreRequest
	if err := decodeRequest(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err, nil, nil)
		return
	}
	user := UserFromContext(r.Context())
	result, err := s.opts.Store.Restore(r.PathValue("name"), request.Version, user)
	s.writeSaveResult(w, result, err, "restore")
}

func (s *Server) writeSaveResult(w http.ResponseWriter, result SaveResult, err error, action string) {
	switch {
	case err == nil:
		writeJSON(w, result)
	case errors.Is(err, ErrConflict):
		var current *ConfigSnapshot
		if snapshot, loadErr := s.opts.Store.Load(); loadErr == nil {
			masked := s.maskSnapshot(snapshot)
			current = &masked
		}
		writeError(w, http.StatusConflict, err, nil, current)
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, err, &result.Validation, nil)
	case errors.Is(err, ErrBackupNotFound):
		writeError(w, http.StatusNotFound, err, nil, nil)
	default:
		log.WithError(err).Errorf("admin: configuration %s failed", action)
		writeError(w, http.StatusInternalServerError, err, nil, nil)
	}
}

func (s *Server) handlePasswordHash(w http.ResponseWriter, r *http.Request) {
	var request passwordRequest
	if err := decodeRequest(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err, nil, nil)
		return
	}
	hash, err := HashPassword(request.Password)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err, nil, nil)
		return
	}
	writeJSON(w, map[string]string{"hash": hash})
}

// decodeRequest reads a bounded JSON body, keeping numbers exact.
func decodeRequest(r *http.Request, into interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		return errors.Wrap(err, "failed to read request")
	}
	if len(body) > maxRequestBody {
		return errors.New("request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(into); err != nil {
		return errors.Wrap(err, "invalid JSON")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, err error, validation *Validation, current *ConfigSnapshot) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: err.Error(), Validation: validation, Current: current})
}
