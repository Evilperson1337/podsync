package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/mxpv/podsync/pkg/audiobookshelf"
	"github.com/mxpv/podsync/pkg/db"
	"github.com/mxpv/podsync/pkg/feed"
	"github.com/mxpv/podsync/pkg/model"
)

type fakeRuntime struct{ feeds []FeedRuntime }

func (r fakeRuntime) Feeds() []FeedRuntime { return r.feeds }

func passwordHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(hash)
}

func newTestServer(t *testing.T, cfg Config, runtime Runtime) (*Server, db.Storage) {
	t.Helper()
	database, err := db.NewBadger(&db.Config{Dir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate())
	if runtime == nil {
		runtime = fakeRuntime{}
	}
	srv, err := New(Options{Config: cfg, Runtime: runtime, DB: database, Version: "test", ConfigPath: "/app/config.toml", Schema: map[string]string{"type": "object"}})
	require.NoError(t, err)
	return srv, database
}

func proxyConfig() Config {
	return Config{Enabled: true, Auth: AuthProxy, TrustedProxies: []string{"10.0.0.0/8", "192.168.1.5"}}
}

func serve(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec
}

func TestConfigValidate(t *testing.T) {
	valid := []Config{
		{},
		{Enabled: false, Auth: "nonsense"},
		{Enabled: true, Port: 8081, Auth: AuthProxy, TrustedProxies: []string{"172.18.0.0/16", "::1"}},
		{Enabled: true, Port: 8081, Auth: AuthPassword, PasswordHash: passwordHash(t, "secret")},
	}
	for _, cfg := range valid {
		assert.NoError(t, cfg.Validate(), "%+v", cfg)
	}

	invalid := map[string]Config{
		"missing auth":        {Enabled: true, Port: 8081},
		"unknown auth":        {Enabled: true, Port: 8081, Auth: "oidc"},
		"proxy without list":  {Enabled: true, Port: 8081, Auth: AuthProxy},
		"bad proxy entry":     {Enabled: true, Port: 8081, Auth: AuthProxy, TrustedProxies: []string{"swag"}},
		"password no hash":    {Enabled: true, Port: 8081, Auth: AuthPassword},
		"password plain text": {Enabled: true, Port: 8081, Auth: AuthPassword, PasswordHash: "hunter2"},
		"bad port":            {Enabled: true, Port: 70000, Auth: AuthProxy, TrustedProxies: []string{"10.0.0.1"}},
	}
	for name, cfg := range invalid {
		assert.Error(t, cfg.Validate(), name)
	}

	cfg := Config{}
	cfg.ApplyDefaults()
	assert.Equal(t, DefaultPort, cfg.Port)
	assert.Equal(t, DefaultUserHeader, cfg.UserHeader)
	assert.Equal(t, DefaultUsername, cfg.Username)
}

func TestProxyAuth(t *testing.T) {
	srv, _ := newTestServer(t, proxyConfig(), nil)

	request := func(remote, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.RemoteAddr = remote
		if user != "" {
			req.Header.Set("Remote-User", user)
		}
		return serve(srv, req)
	}

	rec := request("10.1.2.3:5000", "alice")
	require.Equal(t, http.StatusOK, rec.Code)
	var me meResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	assert.Equal(t, "alice", me.User)
	assert.Equal(t, AuthProxy, me.AuthMode)

	assert.Equal(t, http.StatusOK, request("192.168.1.5:5000", "bob").Code, "single trusted IP")
	assert.Equal(t, http.StatusForbidden, request("192.168.1.6:5000", "bob").Code, "untrusted peer is rejected even with the header")
	assert.Equal(t, http.StatusUnauthorized, request("10.1.2.3:5000", "").Code, "trusted peer without a user is rejected")

	// X-Forwarded-For must not grant trust.
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	req.Header.Set("Remote-User", "mallory")
	assert.Equal(t, http.StatusForbidden, serve(srv, req).Code)
}

func TestProxyAuthCustomHeader(t *testing.T) {
	cfg := proxyConfig()
	cfg.UserHeader = "X-Forwarded-User"
	srv, _ := newTestServer(t, cfg, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.RemoteAddr = "10.0.0.2:1"
	req.Header.Set("Remote-User", "ignored")
	assert.Equal(t, http.StatusUnauthorized, serve(srv, req).Code)

	req.Header.Set("X-Forwarded-User", "carol")
	assert.Equal(t, http.StatusOK, serve(srv, req).Code)
}

func TestPasswordAuth(t *testing.T) {
	srv, _ := newTestServer(t, Config{Enabled: true, Auth: AuthPassword, PasswordHash: passwordHash(t, "correct horse")}, nil)

	request := func(user, password string, withAuth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		if withAuth {
			req.SetBasicAuth(user, password)
		}
		return serve(srv, req)
	}

	rec := request("", "", false)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "Basic")

	assert.Equal(t, http.StatusOK, request("admin", "correct horse", true).Code)
	assert.Equal(t, http.StatusUnauthorized, request("admin", "wrong", true).Code)
	assert.Equal(t, http.StatusUnauthorized, request("root", "correct horse", true).Code)
}

func TestPasswordAuthLockout(t *testing.T) {
	srv, _ := newTestServer(t, Config{Enabled: true, Auth: AuthPassword, PasswordHash: passwordHash(t, "pw")}, nil)
	attempt := func(remote, password string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.RemoteAddr = remote
		req.SetBasicAuth("admin", password)
		return serve(srv, req).Code
	}
	for i := 0; i < 10; i++ {
		assert.Equal(t, http.StatusUnauthorized, attempt("192.0.2.20:1", "nope"))
	}
	assert.Equal(t, http.StatusTooManyRequests, attempt("192.0.2.20:1", "pw"), "locked out even with the right password")
	assert.Equal(t, http.StatusOK, attempt("192.0.2.21:1", "pw"), "other clients are unaffected")
}

func TestFailureLimiterWindow(t *testing.T) {
	now := time.Now()
	limiter := newFailureLimiter(2, time.Minute)
	limiter.now = func() time.Time { return now }
	limiter.fail("c")
	limiter.fail("c")
	assert.True(t, limiter.blocked("c"))
	now = now.Add(2 * time.Minute)
	assert.False(t, limiter.blocked("c"), "failures expire after the window")
}

func TestRequireSameOrigin(t *testing.T) {
	handler := requireSameOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	check := func(method string, headers map[string]string) int {
		req := httptest.NewRequest(method, "http://podsync-admin.example.com/api/thing", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusNoContent, check(http.MethodGet, nil), "reads need no token")
	assert.Equal(t, http.StatusForbidden, check(http.MethodPost, nil), "writes need the header")
	assert.Equal(t, http.StatusNoContent, check(http.MethodPost, map[string]string{csrfHeader: "1", "Origin": "https://podsync-admin.example.com"}))
	assert.Equal(t, http.StatusForbidden, check(http.MethodPost, map[string]string{csrfHeader: "1", "Origin": "https://evil.example.net"}))
	assert.Equal(t, http.StatusForbidden, check(http.MethodPut, map[string]string{csrfHeader: "1", "Sec-Fetch-Site": "cross-site"}))
}

func TestUIAndSecurityHeaders(t *testing.T) {
	srv, _ := newTestServer(t, proxyConfig(), nil)
	for _, path := range []string{"/", "/app.js", "/app.css"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "10.0.0.2:1"
		req.Header.Set("Remote-User", "alice")
		rec := serve(srv, req)
		require.Equal(t, http.StatusOK, rec.Code, path)
		assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'self'", path)
		assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	}

	// Unauthenticated responses carry the headers too.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.1:1"
	rec := serve(srv, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
}

func TestStatusEndpoint(t *testing.T) {
	next := time.Now().Add(time.Hour).UTC()
	runtime := fakeRuntime{feeds: []FeedRuntime{
		{Config: &feed.Config{ID: "zeta", URL: "https://example.com/z", Format: model.FormatVideo}, Schedule: "@every 6h0m0s"},
		{Config: &feed.Config{ID: "alpha", URL: "https://example.com/a", Format: model.FormatAudio,
			Audiobookshelf: audiobookshelf.FeedConfig{Enabled: true, Directory: "Alpha"}}, Schedule: "0 3 * * *", NextRun: next},
	}}
	srv, database := newTestServer(t, proxyConfig(), runtime)

	success := time.Now().Add(-2 * time.Hour).UTC()
	require.NoError(t, database.AddFeed(t.Context(), "alpha", &model.Feed{
		ID:            "alpha",
		Title:         "Alpha Show",
		LastSuccessAt: success,
		Episodes: []*model.Episode{
			{ID: "1", Status: model.EpisodePublished, AudiobookshelfLink: &model.HardlinkRecord{Path: "/p/1.mp3", Inode: 1}},
			{ID: "2", Status: model.EpisodePublished},
			{ID: "3", Status: model.EpisodeError},
		},
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.RemoteAddr = "10.0.0.2:1"
	req.Header.Set("Remote-User", "alice")
	rec := serve(srv, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var status Status
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	assert.Equal(t, "test", status.Version)
	assert.Equal(t, "/app/config.toml", status.ConfigPath)
	require.Len(t, status.Feeds, 2)
	assert.Equal(t, "alpha", status.Feeds[0].ID, "feeds are sorted by ID")

	alpha := status.Feeds[0]
	assert.True(t, alpha.Synced)
	assert.Equal(t, "Alpha Show", alpha.Title)
	assert.Equal(t, map[string]int{"published": 2, "error": 1}, alpha.Episodes)
	require.NotNil(t, alpha.NextRun)
	assert.WithinDuration(t, next, *alpha.NextRun, time.Second)
	require.NotNil(t, alpha.LastSuccessAt)
	require.NotNil(t, alpha.Audiobookshelf)
	assert.Equal(t, "Alpha", alpha.Audiobookshelf.Directory)
	assert.Equal(t, 1, alpha.Audiobookshelf.Linked)

	zeta := status.Feeds[1]
	assert.False(t, zeta.Synced, "a feed that never synced has no stored data")
	assert.Nil(t, zeta.NextRun)
	assert.Nil(t, zeta.Audiobookshelf)
	assert.Empty(t, zeta.Episodes)
}

func TestSchemaEndpoint(t *testing.T) {
	srv, _ := newTestServer(t, proxyConfig(), nil)
	req := httptest.NewRequest(http.MethodGet, "/api/schema", nil)
	req.RemoteAddr = "10.0.0.2:1"
	req.Header.Set("Remote-User", "alice")
	rec := serve(srv, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"type":"object"}`, strings.TrimSpace(rec.Body.String()))
}
