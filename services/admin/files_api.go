package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

// Files gives the editor access to files that configuration options refer to.
type Files interface {
	// AudiobookshelfDirectories lists podcast directories under the running podcast_root.
	AudiobookshelfDirectories() (DirectoryListing, error)
	// SignatureFiles lists signature audio files for a feed.
	SignatureFiles(feedID string) (SignatureListing, error)
	// SaveSignatureFile stores an uploaded signature file for a feed. It returns
	// ErrFileExists when the name is taken and replace is false.
	SaveSignatureFile(feedID, name string, content io.Reader, replace bool) (SignatureFile, error)
}

var (
	// ErrFileExists means an upload would replace an existing file.
	ErrFileExists = errors.New("a file with this name already exists")
	// ErrInvalidFile means an upload was rejected (name, type or content).
	ErrInvalidFile = errors.New("invalid file")
	// ErrNotConfigured means a listing needs a setting that is not set.
	ErrNotConfigured = errors.New("not configured")
)

// DirectoryListing lists directories under a root.
type DirectoryListing struct {
	Root        string   `json:"root"`
	Directories []string `json:"directories"`
}

// SignatureListing lists signature files in a feed's signatures directory.
type SignatureListing struct {
	Directory string          `json:"directory"`
	Files     []SignatureFile `json:"files"`
}

// SignatureFile is a signature audio file.
type SignatureFile struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

// MaxSignatureUpload bounds signature uploads; signature clips are seconds long.
const MaxSignatureUpload = 50 << 20

func (s *Server) registerFileRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/audiobookshelf/directories", s.handleAudiobookshelfDirectories)
	mux.HandleFunc("GET /api/feeds/{id}/signatures", s.handleSignatureFiles)
	mux.HandleFunc("POST /api/feeds/{id}/signatures", s.handleUploadSignature)
}

func (s *Server) handleAudiobookshelfDirectories(w http.ResponseWriter, _ *http.Request) {
	listing, err := s.opts.Files.AudiobookshelfDirectories()
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeJSON(w, listing)
}

func (s *Server) handleSignatureFiles(w http.ResponseWriter, r *http.Request) {
	listing, err := s.opts.Files.SignatureFiles(r.PathValue("id"))
	if err != nil {
		writeFileError(w, err)
		return
	}
	writeJSON(w, listing)
}

func (s *Server) handleUploadSignature(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxSignatureUpload+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(w, http.StatusBadRequest, errors.Wrap(err, "upload must be multipart form data within the size limit"), nil, nil)
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.Wrap(err, "missing file"), nil, nil)
		return
	}
	defer file.Close()
	if header.Size > MaxSignatureUpload {
		writeError(w, http.StatusRequestEntityTooLarge, errors.Errorf("signature files are limited to %d MB", MaxSignatureUpload>>20), nil, nil)
		return
	}

	feedID := r.PathValue("id")
	saved, err := s.opts.Files.SaveSignatureFile(feedID, header.Filename, file, r.FormValue("replace") == "true")
	if err != nil {
		writeFileError(w, err)
		return
	}
	log.WithFields(log.Fields{"feed_id": feedID, "file": saved.Name, "size": saved.Size, "user": UserFromContext(r.Context())}).Info("signature file uploaded via the admin interface")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(saved)
}

func writeFileError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrFileExists):
		status = http.StatusConflict
	case errors.Is(err, ErrInvalidFile):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, ErrNotConfigured):
		status = http.StatusConflict
	}
	writeError(w, status, err, nil, nil)
}
