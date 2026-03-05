package update

import "testing"

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
