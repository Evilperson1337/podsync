package audiosig

import (
	"testing"
	"time"
)

// TestFormatFFmpegTime verifies ffmpeg time formatting.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Ensures millisecond precision.
func TestFormatFFmpegTime(t *testing.T) {
	got := formatFFmpegTime(1500 * time.Millisecond)
	if got != "1.500" {
		t.Fatalf("expected 1.500, got %s", got)
	}
}

// TestParseBitrateValueEmpty verifies errors on empty output.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Empty strings should be rejected.
func TestParseBitrateValueEmpty(t *testing.T) {
	if _, err := parseBitrateValue("\n"); err == nil {
		t.Fatalf("expected error for empty bitrate")
	}
}
