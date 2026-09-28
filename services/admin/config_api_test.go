package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/mxpv/podsync/pkg/configschema"
)

func editorSchema() *configschema.Schema {
	secretTokens := &configschema.Schema{Type: []string{"string", "array"}, Secret: true}
	return &configschema.Schema{
		Type: "object",
		Properties: map[string]*configschema.Schema{
			"server": {Type: "object", Properties: map[string]*configschema.Schema{"port": {Type: "integer"}}},
			"tokens": {Type: "object", Secret: true, AdditionalProperties: secretTokens},
			"admin": {Type: "object", Properties: map[string]*configschema.Schema{
				"password_hash": {Type: "string", Secret: true},
			}},
		},
	}
}

func TestMaskAndRestoreSecrets(t *testing.T) {
	document := map[string]interface{}{
		"server": map[string]interface{}{"port": int64(8080)},
		"tokens": map[string]interface{}{"youtube": []interface{}{"k1", "k2"}, "vimeo": "v1"},
		"admin":  map[string]interface{}{"password_hash": "$2a$10$abc"},
	}
	masked := maskSecrets(document, editorSchema()).(map[string]interface{})
	assert.Equal(t, map[string]interface{}{"youtube": SecretPlaceholder, "vimeo": SecretPlaceholder}, masked["tokens"], "provider names stay visible, values do not")
	assert.Equal(t, SecretPlaceholder, masked["admin"].(map[string]interface{})["password_hash"])
	assert.Equal(t, int64(8080), masked["server"].(map[string]interface{})["port"])
	assert.Equal(t, []interface{}{"k1", "k2"}, document["tokens"].(map[string]interface{})["youtube"], "the original is not modified")

	// Unchanged placeholders restore the current values; replaced values are kept.
	submitted := maskSecrets(document, editorSchema()).(map[string]interface{})
	submitted["tokens"].(map[string]interface{})["vimeo"] = "new-key"
	restored, err := restoreSecrets(submitted, document, nil)
	require.NoError(t, err)
	tokens := restored.(map[string]interface{})["tokens"].(map[string]interface{})
	assert.Equal(t, []interface{}{"k1", "k2"}, tokens["youtube"])
	assert.Equal(t, "new-key", tokens["vimeo"])
	assert.Equal(t, "$2a$10$abc", restored.(map[string]interface{})["admin"].(map[string]interface{})["password_hash"])

	// A placeholder for a secret that does not exist cannot be kept.
	submitted["tokens"].(map[string]interface{})["twitch"] = SecretPlaceholder
	_, err = restoreSecrets(submitted, document, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tokens.twitch")
}

type fakeStore struct {
	snapshot   ConfigSnapshot
	validated  map[string]interface{}
	saved      map[string]interface{}
	savedUser  string
	saveErr    error
	validation Validation
	restored   string
}

func (f *fakeStore) Load() (ConfigSnapshot, error) { return f.snapshot, nil }

func (f *fakeStore) Validate(document map[string]interface{}) (Validation, error) {
	f.validated = document
	return f.validation, nil
}

func (f *fakeStore) Save(document map[string]interface{}, version string, user string) (SaveResult, error) {
	if version != f.snapshot.Version {
		return SaveResult{}, ErrConflict
	}
	if f.saveErr != nil {
		return SaveResult{Validation: f.validation}, f.saveErr
	}
	f.saved, f.savedUser = document, user
	return SaveResult{Version: "v2", Backup: "config.toml.bak.x", FeedsAdded: []string{"new"}}, nil
}

func (f *fakeStore) Backups() ([]Backup, error) { return nil, nil }

func (f *fakeStore) Restore(name, version, user string) (SaveResult, error) {
	if name != "config.toml.bak.1" {
		return SaveResult{}, ErrBackupNotFound
	}
	f.restored = name
	return SaveResult{Version: "v3"}, nil
}

func newEditorServer(t *testing.T, store *fakeStore) *Server {
	t.Helper()
	cfg := proxyConfig()
	cfg.ApplyDefaults()
	srv, err := New(Options{Config: cfg, Runtime: fakeRuntime{}, Schema: editorSchema(), Store: store})
	require.NoError(t, err)
	return srv
}

func editorRequest(method, path, body string, csrf bool) *http.Request {
	req := httptest.NewRequest(method, "http://admin.example.com"+path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.2:1"
	req.Header.Set("Remote-User", "alice")
	req.Header.Set("Content-Type", "application/json")
	if csrf {
		req.Header.Set(csrfHeader, "1")
		req.Header.Set("Origin", "http://admin.example.com")
	}
	return req
}

func sampleStore() *fakeStore {
	return &fakeStore{
		snapshot: ConfigSnapshot{
			Path: "/app/config.toml", Format: "toml", Version: "v1",
			Document: map[string]interface{}{
				"server": map[string]interface{}{"port": int64(8080)},
				"tokens": map[string]interface{}{"youtube": "secret-key"},
			},
			EnvOverrides: []EnvOverride{{Path: []string{"server", "port"}, Variable: "PODSYNC__SERVER__PORT"}},
		},
		validation: Validation{Valid: true, Preview: "[server]\nport = 9000\n"},
	}
}

func TestGetConfigMasksSecrets(t *testing.T) {
	srv := newEditorServer(t, sampleStore())
	rec := serve(srv, editorRequest(http.MethodGet, "/api/config", "", false))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "secret-key")

	var snapshot ConfigSnapshot
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &snapshot))
	assert.Equal(t, "v1", snapshot.Version)
	assert.Equal(t, SecretPlaceholder, snapshot.Document["tokens"].(map[string]interface{})["youtube"])
	assert.Equal(t, "PODSYNC__SERVER__PORT", snapshot.EnvOverrides[0].Variable)
}

func TestValidateConfigRestoresSecrets(t *testing.T) {
	store := sampleStore()
	srv := newEditorServer(t, store)
	body := `{"document": {"server": {"port": 9000}, "tokens": {"youtube": "` + SecretPlaceholder + `"}}}`
	rec := serve(srv, editorRequest(http.MethodPost, "/api/config/validate", body, true))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "secret-key", store.validated["tokens"].(map[string]interface{})["youtube"], "the store sees the real secret")
	assert.Equal(t, json.Number("9000"), store.validated["server"].(map[string]interface{})["port"], "numbers stay exact")

	var validation Validation
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &validation))
	assert.True(t, validation.Valid)
}

func TestSaveConfig(t *testing.T) {
	store := sampleStore()
	srv := newEditorServer(t, store)
	body := `{"version": "v1", "document": {"server": {"port": 9000}, "tokens": {"youtube": "` + SecretPlaceholder + `"}}}`

	assert.Equal(t, http.StatusForbidden, serve(srv, editorRequest(http.MethodPut, "/api/config", body, false)).Code, "writes need the same-origin header")

	rec := serve(srv, editorRequest(http.MethodPut, "/api/config", body, true))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "alice", store.savedUser)
	assert.Equal(t, "secret-key", store.saved["tokens"].(map[string]interface{})["youtube"])
	var result SaveResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, "v2", result.Version)
	assert.Equal(t, []string{"new"}, result.FeedsAdded)
}

func TestSaveConfigConflict(t *testing.T) {
	srv := newEditorServer(t, sampleStore())
	rec := serve(srv, editorRequest(http.MethodPut, "/api/config", `{"version": "stale", "document": {}}`, true))
	require.Equal(t, http.StatusConflict, rec.Code)

	var response errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.NotNil(t, response.Current, "a conflict returns the current file so the editor can reload")
	assert.Equal(t, "v1", response.Current.Version)
	assert.NotContains(t, rec.Body.String(), "secret-key", "the current file is masked too")
}

func TestSaveConfigInvalid(t *testing.T) {
	store := sampleStore()
	store.saveErr = ErrInvalid
	store.validation = Validation{Valid: false, Errors: []string{"URL is required for \"show\""}}
	srv := newEditorServer(t, store)

	rec := serve(srv, editorRequest(http.MethodPut, "/api/config", `{"version": "v1", "document": {}}`, true))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	var response errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.NotNil(t, response.Validation)
	assert.Equal(t, []string{"URL is required for \"show\""}, response.Validation.Errors)
}

func TestSaveConfigUnknownSecretPlaceholder(t *testing.T) {
	srv := newEditorServer(t, sampleStore())
	body := `{"version": "v1", "document": {"tokens": {"vimeo": "` + SecretPlaceholder + `"}}}`
	rec := serve(srv, editorRequest(http.MethodPut, "/api/config", body, true))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Contains(t, rec.Body.String(), "tokens.vimeo")
}

func TestRestoreBackup(t *testing.T) {
	store := sampleStore()
	srv := newEditorServer(t, store)
	rec := serve(srv, editorRequest(http.MethodPost, "/api/config/backups/config.toml.bak.1/restore", `{"version": "v1"}`, true))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "config.toml.bak.1", store.restored)

	rec = serve(srv, editorRequest(http.MethodPost, "/api/config/backups/missing/restore", `{"version": "v1"}`, true))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	rec = serve(srv, editorRequest(http.MethodGet, "/api/config/backups", "", false))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `[]`, rec.Body.String())
}

func TestPasswordHashEndpoint(t *testing.T) {
	srv := newEditorServer(t, sampleStore())
	rec := serve(srv, editorRequest(http.MethodPost, "/api/password-hash", `{"password": "short"}`, true))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	rec = serve(srv, editorRequest(http.MethodPost, "/api/password-hash", `{"password": "a long enough password"}`, true))
	require.Equal(t, http.StatusOK, rec.Code)
	var response map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(response["hash"]), []byte("a long enough password")))
}

func TestEditorDisabledWithoutStore(t *testing.T) {
	srv, _ := newTestServer(t, proxyConfig(), nil)
	rec := serve(srv, editorRequest(http.MethodGet, "/api/config", "", false))
	assert.NotEqual(t, http.StatusOK, rec.Code, "the editor API only exists with a store")

	rec = serve(srv, editorRequest(http.MethodGet, "/api/me", "", false))
	var me meResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	assert.False(t, me.Editable)
}

func TestRequestBodyLimit(t *testing.T) {
	srv := newEditorServer(t, sampleStore())
	huge := `{"document": {"x": "` + strings.Repeat("a", maxRequestBody) + `"}}`
	rec := serve(srv, editorRequest(http.MethodPost, "/api/config/validate", huge, true))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "too large")
}
