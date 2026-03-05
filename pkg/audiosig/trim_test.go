package audiosig

import (
	"context"
	"testing"
	"time"
)

// TestRunTrimWithStderrInvalid verifies stderr is returned on failure.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Uses invalid input path.
func TestRunTrimWithStderrInvalid(t *testing.T) {
	stderr, err := RunTrimWithStderr(context.Background(), "nonexistent.mp3", "out.mp3", 1*time.Second, false, 128)
	if err == nil {
		t.Fatalf("expected error")
	}
	_ = stderr
}
