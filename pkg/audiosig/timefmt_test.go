package audiosig

import (
	"testing"
	"time"
)

// TestFormatHMS verifies HH:MM:SS formatting.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Truncates sub-second precision.
func TestFormatHMS(t *testing.T) {
	got := FormatHMS(3661*time.Second + 900*time.Millisecond)
	if got != "01:01:01" {
		t.Fatalf("expected 01:01:01, got %s", got)
	}
}

// TestFormatHMSMillis verifies HH:MM:SS.mmm formatting.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Rounds to nearest millisecond.
func TestFormatHMSMillis(t *testing.T) {
	got := FormatHMSMillis(1*time.Second + 234*time.Millisecond)
	if got != "00:00:01.234" {
		t.Fatalf("expected 00:00:01.234, got %s", got)
	}
}
