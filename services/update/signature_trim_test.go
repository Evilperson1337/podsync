package update

import (
	"testing"

	"github.com/mxpv/podsync/pkg/model"
)

// TestFindSignatureFileEmpty verifies empty feed ID behavior.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Should return empty path without error.
func TestFindSignatureFileEmpty(t *testing.T) {
	u := &Manager{}
	path, err := u.findSignatureFile("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" {
		t.Fatalf("expected empty path, got %s", path)
	}
}

// TestFindSignatureFileEmptyDir verifies missing directory returns empty path.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Uses a non-existent dir.
func TestFindSignatureFileEmptyDir(t *testing.T) {
	u := &Manager{sigDir: "/path/does/not/exist"}
	path, err := u.findSignatureFile("crowder")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" {
		t.Fatalf("expected empty path, got %s", path)
	}
}

func TestSponsorBlockVideoID(t *testing.T) {
	if got := sponsorBlockVideoID(&model.Episode{ID: "v76ws1m"}); got != "v76ws1m" {
		t.Fatalf("unexpected id from episode id: %s", got)
	}
	if got := sponsorBlockVideoID(&model.Episode{VideoURL: "https://rumble.com/v76ws1m-crowder.html"}); got != "v76ws1m" {
		t.Fatalf("unexpected id from video url: %s", got)
	}
}
