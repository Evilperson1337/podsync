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

func TestSeparatedPeaks(t *testing.T) {
	scores := []float64{0.1, 0.9, 0.85, 0.2, 0.1, 0.8, 0.7, 0.1, 0.1, 0.6}

	peaks := SeparatedPeaks(scores, 3, 10)
	offsets := make([]int, 0, len(peaks))
	for _, peak := range peaks {
		offsets = append(offsets, peak.Offset)
	}
	// 2 is suppressed by 1, 6 by 5; 9 is far enough from 5.
	if len(offsets) != 3 || offsets[0] != 1 || offsets[1] != 5 || offsets[2] != 9 {
		t.Fatalf("unexpected peaks: %v", offsets)
	}

	if got := SeparatedPeaks(scores, 3, 2); len(got) != 2 {
		t.Fatalf("expected limit to cap peaks, got %d", len(got))
	}
	if got := SeparatedPeaks(nil, 3, 2); len(got) != 0 {
		t.Fatalf("expected no peaks for empty scores, got %d", len(got))
	}
}
