package audiosig

import "testing"

// TestParseBitrateValue verifies parsing of ffprobe bitrate output.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Expects integer bitrate in bps.
func TestParseBitrateValue(t *testing.T) {
	bitrate, err := parseBitrateValue("128000\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bitrate != 128000 {
		t.Fatalf("expected 128000, got %d", bitrate)
	}
}
