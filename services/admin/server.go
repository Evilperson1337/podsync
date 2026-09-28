// Package admin serves the Podsync admin interface: a separate, authenticated listener with a
// dashboard and JSON API, kept apart from the public podcast server so a reverse proxy can
// protect it without affecting feed and episode URLs.
package admin

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/configschema"
	"github.com/mxpv/podsync/pkg/db"
)

//go:embed ui
var uiFiles embed.FS

// Options configures the admin server.
type Options struct {
	Config     Config
	Runtime    Runtime
	DB         db.Storage
	Version    string
	ConfigPath string
	// Schema is the JSON Schema of the configuration, served at /api/schema and used to mask
	// secrets.
	Schema *configschema.Schema
	// Store enables the configuration editor; without it the interface is read-only.
	Store ConfigStore
}

// Server is the admin HTTP server.
type Server struct {
	http.Server
	opts Options
}

// New creates the admin server. Config must already be validated.
func New(opts Options) (*Server, error) {
	auth, err := newAuthenticator(opts.Config)
	if err != nil {
		return nil, err
	}
	static, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		return nil, errors.Wrap(err, "failed to load admin UI")
	}

	srv := &Server{opts: opts}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/me", srv.handleMe)
	mux.HandleFunc("GET /api/status", srv.handleStatus)
	mux.HandleFunc("GET /api/schema", srv.handleSchema)
	if opts.Store != nil {
		srv.registerConfigRoutes(mux)
	}
	mux.Handle("GET /", http.FileServerFS(static))

	bind := opts.Config.BindAddress
	if bind == "*" {
		bind = ""
	}
	srv.Addr = fmt.Sprintf("%s:%d", bind, opts.Config.Port)
	srv.Handler = securityHeaders(auth.wrap(requireSameOrigin(mux)))
	return srv, nil
}

type meResponse struct {
	User     string `json:"user"`
	AuthMode string `json:"auth_mode"`
	Version  string `json:"version"`
	Editable bool   `json:"editable"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, meResponse{User: UserFromContext(r.Context()), AuthMode: s.opts.Config.Auth, Version: s.opts.Version, Editable: s.opts.Store != nil})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := buildStatus(r.Context(), s.opts.DB, s.opts.Runtime)
	status.Version = s.opts.Version
	status.ConfigPath = s.opts.ConfigPath
	writeJSON(w, status)
}

func (s *Server) handleSchema(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.opts.Schema)
}

func writeJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.WithError(err).Warn("admin: failed to write response")
	}
}
