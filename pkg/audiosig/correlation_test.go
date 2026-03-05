package audiosig

import (
	"math"
	"testing"
)

// TestCorrelateNormalizedDetectsOffset verifies that correlation finds the right offset.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Uses a simple synthetic signal with embedded pattern.
func TestCorrelateNormalizedDetectsOffset(t *testing.T) {
	signal := make([]float64, 200)
	pattern := make([]float64, 20)
	for i := range pattern {
		pattern[i] = math.Sin(float64(i) / 3)
	}
	offset := 73
	for i := range pattern {
		signal[offset+i] = pattern[i]
	}
	scores := CorrelateNormalized(signal, pattern)
	peaks := TopKPeaks(scores, 1)
	if len(peaks) == 0 {
		t.Fatalf("expected a peak")
	}
	if peaks[0].Offset != offset {
		t.Fatalf("expected offset %d, got %d", offset, peaks[0].Offset)
	}
}

// TestBestPeakRatio verifies peak ratio computation.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Uses fixed peaks.
func TestBestPeakRatio(t *testing.T) {
	peaks := []Peak{{Offset: 1, Score: 0.9}, {Offset: 2, Score: 0.3}}
	ratio := BestPeakRatio(peaks)
	if math.Abs(ratio-3.0) > 1e-6 {
		t.Fatalf("expected ratio 3.0, got %f", ratio)
	}
}
