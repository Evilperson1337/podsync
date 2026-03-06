package audiosig

import "testing"

// TestMatchDecision verifies threshold logic.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Checks both score and ratio thresholds.
func TestMatchDecision(t *testing.T) {
	if MatchDecision(0.5, 1.5, 0.6, 1.2) {
		t.Fatalf("expected false when score below min")
	}
	if MatchDecision(0.7, 1.0, 0.6, 1.2) {
		t.Fatalf("expected false when ratio below min")
	}
	if !MatchDecision(0.7, 1.3, 0.6, 1.2) {
		t.Fatalf("expected true when thresholds satisfied")
	}
}
