package web

import (
	"encoding/json"
	"expvar"
	"fmt"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/model"
)

type Server struct {
	http.Server
	db db.Storage
}

type Config struct {
	// Hostname to use for download links
	Hostname string `toml:"hostname"`
	// Port is a server port to listen to
	Port int `toml:"port"`
	// Bind a specific IP addresses for server
	// "*": bind all IP addresses which is default option
	// localhost or 127.0.0.1  bind a single IPv4 address
	BindAddress string `toml:"bind_address"`
	// Flag indicating if the server will use TLS
	TLS bool `toml:"tls"`
	// Path to a certificate file for TLS connections
	CertificatePath string `toml:"certificate_path"`
	// Path to a private key file for TLS connections
	KeyFilePath string `toml:"key_file_path"`
	// Specify path for reverse proxy and only [A-Za-z0-9]
	Path string `toml:"path"`
	// DataDir is a path to a directory to keep XML feeds and downloaded episodes,
	// that will be available to user via web server for download.
	DataDir string `toml:"data_dir"`
	// WebUIEnabled is a flag indicating if web UI is enabled
	WebUIEnabled bool `toml:"web_ui"`
	// DebugEndpoints enables /debug/vars endpoint for runtime metrics (disabled by default)
	DebugEndpoints bool `toml:"debug_endpoints"`
}

func New(cfg Config, storage http.FileSystem, database db.Storage) *Server {
	port := cfg.Port
	if port == 0 {
		port = 8080
	}

	bindAddress := cfg.BindAddress
	if bindAddress == "*" {
		bindAddress = ""
	}

	srv := Server{
		db: database,
	}

	srv.Addr = fmt.Sprintf("%s:%d", bindAddress, port)
	log.Debugf("using address: %s:%s", bindAddress, srv.Addr)

	// Use a custom mux instead of http.DefaultServeMux to avoid exposing
	// debug endpoints registered by imported packages (security fix for #799)
	mux := http.NewServeMux()

	fileServer := http.FileServer(storage)

	log.Debugf("handle path: /%s", cfg.Path)
	mux.Handle(fmt.Sprintf("/%s", cfg.Path), fileServer)

	// Add health check endpoint
	mux.HandleFunc("/health", srv.healthCheckHandler)

	// Optionally enable debug endpoints (disabled by default for security)
	if cfg.DebugEndpoints {
		log.Info("debug endpoints enabled at /debug/vars")
		mux.Handle("/debug/vars", expvar.Handler())
	}

	srv.Handler = mux

	return &srv
}

type HealthStatus struct {
	Status            string         `json:"status"`
	Timestamp         time.Time      `json:"timestamp"`
	FailedEpisodes    int            `json:"failed_episodes,omitempty"`
	FailureCategories map[string]int `json:"failure_categories,omitempty"`
	Message           string         `json:"message,omitempty"`
}

func (s *Server) healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status, err := s.healthStatus(r)

	if err != nil {
		log.WithError(err).Error("health check database error")
		status.Status = "unhealthy"
		status.Message = "database error during health check"
		w.WriteHeader(http.StatusServiceUnavailable)
	} else if status.Status == "unhealthy" {
		w.WriteHeader(http.StatusServiceUnavailable)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	json.NewEncoder(w).Encode(status)
}

func (s *Server) healthStatus(r *http.Request) (HealthStatus, error) {
	ctx := r.Context()
	if summary, err := s.db.GetHealthSummary(ctx); err == nil && time.Since(summary.ComputedAt) < 5*time.Second {
		log.WithFields(log.Fields{"computed_at": summary.ComputedAt, "failed_episodes": summary.FailedEpisodes}).Debug("using persisted health summary cache")
		return HealthStatus{
			Status:            summary.Status,
			Timestamp:         time.Now(),
			FailedEpisodes:    summary.FailedEpisodes,
			FailureCategories: summary.FailureCategories,
			Message:           summary.Message,
		}, nil
	}

	failedCount := 0
	failureCategories := map[string]int{}
	cutoffTime := time.Now().Add(-24 * time.Hour)
	err := s.db.WalkFeeds(ctx, func(feed *model.Feed) error {
		return s.db.WalkEpisodes(ctx, feed.ID, func(episode *model.Episode) error {
			if episode.Status == model.EpisodeError && !episode.LastErrorAt.IsZero() && episode.LastErrorAt.After(cutoffTime) {
				failedCount++
				category := episode.FailureCategory
				if category == "" {
					category = model.FailureCategoryUnknown
				}
				failureCategories[category]++
			}
			return nil
		})
	})
	if err != nil {
		return HealthStatus{Timestamp: time.Now()}, err
	}

	status := HealthStatus{Timestamp: time.Now()}
	if failedCount > 0 {
		status.Status = "unhealthy"
		status.FailedEpisodes = failedCount
		status.FailureCategories = failureCategories
		status.Message = fmt.Sprintf("found %d failed downloads in the last 24 hours", failedCount)
	} else {
		status.Status = "healthy"
		status.Message = "no recent download failures detected"
	}
	log.WithFields(log.Fields{"status": status.Status, "failed_episodes": status.FailedEpisodes, "failure_categories": status.FailureCategories}).Info("refreshing persisted health summary")
	_ = s.db.SetHealthSummary(ctx, &model.HealthSummary{
		Status:            status.Status,
		Timestamp:         status.Timestamp,
		FailedEpisodes:    status.FailedEpisodes,
		FailureCategories: status.FailureCategories,
		Message:           status.Message,
		ComputedAt:        time.Now(),
	})
	return status, nil
}
