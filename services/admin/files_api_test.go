package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeFiles struct {
	uploaded map[string]string
	replace  bool
}

func (f *fakeFiles) AudiobookshelfDirectories() (DirectoryListing, error) {
	return DirectoryListing{Root: "/podcasts", Directories: []string{"Alpha", "Beta"}}, nil
}

func (f *fakeFiles) SignatureFiles(feedID string) (SignatureListing, error) {
	if feedID == "unset" {
		return SignatureListing{}, ErrNotConfigured
	}
	return SignatureListing{Directory: "/data/" + feedID + "/signatures", Files: []SignatureFile{{Name: "intro.wav", Size: 10}}}, nil
}

func (f *fakeFiles) SaveSignatureFile(feedID, name string, content io.Reader, replace bool) (SignatureFile, error) {
	if name == "bad.txt" {
		return SignatureFile{}, ErrInvalidFile
	}
	if _, ok := f.uploaded[name]; ok && !replace {
		return SignatureFile{}, ErrFileExists
	}
	data, _ := io.ReadAll(content)
	f.uploaded[name] = string(data)
	f.replace = replace
	return SignatureFile{Name: name, Size: int64(len(data)), ModifiedAt: time.Now()}, nil
}

func newFilesServer(t *testing.T, files *fakeFiles) *Server {
	t.Helper()
	cfg := proxyConfig()
	cfg.ApplyDefaults()
	srv, err := New(Options{Config: cfg, Runtime: fakeRuntime{}, Files: files})
	require.NoError(t, err)
	return srv
}

func uploadRequest(t *testing.T, name, content string, replace bool) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	require.NoError(t, err)
	_, _ = part.Write([]byte(content))
	if replace {
		require.NoError(t, writer.WriteField("replace", "true"))
	}
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/feeds/show/signatures", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.RemoteAddr = "10.0.0.2:1"
	req.Header.Set("Remote-User", "alice")
	req.Header.Set(csrfHeader, "1")
	return req
}

func TestFileEndpoints(t *testing.T) {
	files := &fakeFiles{uploaded: map[string]string{}}
	srv := newFilesServer(t, files)

	rec := serve(srv, editorRequest(http.MethodGet, "/api/audiobookshelf/directories", "", false))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"root":"/podcasts","directories":["Alpha","Beta"]}`, rec.Body.String())

	rec = serve(srv, editorRequest(http.MethodGet, "/api/feeds/show/signatures", "", false))
	require.Equal(t, http.StatusOK, rec.Code)
	var listing SignatureListing
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listing))
	assert.Equal(t, "intro.wav", listing.Files[0].Name)

	assert.Equal(t, http.StatusConflict, serve(srv, editorRequest(http.MethodGet, "/api/feeds/unset/signatures", "", false)).Code)

	rec = serve(srv, uploadRequest(t, "clip.wav", "RIFF", false))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "RIFF", files.uploaded["clip.wav"])

	assert.Equal(t, http.StatusConflict, serve(srv, uploadRequest(t, "clip.wav", "again", false)).Code, "an existing file is not replaced silently")
	assert.Equal(t, http.StatusCreated, serve(srv, uploadRequest(t, "clip.wav", "again", true)).Code)
	assert.True(t, files.replace)
	assert.Equal(t, http.StatusUnprocessableEntity, serve(srv, uploadRequest(t, "bad.txt", "x", false)).Code)

	noCSRF := uploadRequest(t, "clip2.wav", "x", false)
	noCSRF.Header.Del(csrfHeader)
	assert.Equal(t, http.StatusForbidden, serve(srv, noCSRF).Code, "uploads need the same-origin header")
}

func TestFileEndpointsAbsentWithoutFiles(t *testing.T) {
	srv, _ := newTestServer(t, proxyConfig(), nil)
	rec := serve(srv, editorRequest(http.MethodGet, "/api/audiobookshelf/directories", "", false))
	assert.NotEqual(t, http.StatusOK, rec.Code)
}
