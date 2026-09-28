package audiosig

import (
	"math"
	"math/cmplx"
	"math/rand"
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

func TestCorrelateNormalizedFFTMatchesDirect(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, size := range []struct{ n, m int }{{1000, 256}, {5000, 300}, {4096, 1024}, {3000, 3000}} {
		signal := make([]float64, size.n)
		for i := range signal {
			signal[i] = rng.NormFloat64()
		}
		// A silent stretch exercises the low-energy guard.
		for i := size.n / 3; i < size.n/3+size.n/10; i++ {
			signal[i] = 0
		}
		pattern := make([]float64, size.m)
		for i := range pattern {
			pattern[i] = rng.NormFloat64()
		}
		plant := (size.n - size.m) / 2
		copy(signal[plant:], pattern)

		patEnergy := 0.0
		for _, v := range pattern {
			patEnergy += v * v
		}
		patEnergy = math.Sqrt(patEnergy)
		direct := correlateNormalizedDirect(signal, pattern, patEnergy)
		viaFFT := correlateNormalizedFFT(signal, pattern, patEnergy)
		if len(direct) != len(viaFFT) {
			t.Fatalf("n=%d m=%d: length mismatch %d vs %d", size.n, size.m, len(direct), len(viaFFT))
		}
		for i := range direct {
			if math.Abs(direct[i]-viaFFT[i]) > 1e-9 {
				t.Fatalf("n=%d m=%d: score mismatch at %d: direct=%g fft=%g", size.n, size.m, i, direct[i], viaFFT[i])
			}
		}
		if best := TopKPeaks(viaFFT, 1); best[0].Offset != plant || math.Abs(best[0].Score-1) > 1e-9 {
			t.Fatalf("n=%d m=%d: expected exact match at %d, got %+v", size.n, size.m, plant, best[0])
		}
	}
}

func TestFFTRoundTrip(t *testing.T) {
	values := []complex128{1, 2, 3, 4, 0, -1, 0.5, 2}
	original := append([]complex128(nil), values...)
	fft(values, false)
	fft(values, true)
	for i := range values {
		if cmplx.Abs(values[i]/complex(float64(len(values)), 0)-original[i]) > 1e-12 {
			t.Fatalf("round trip mismatch at %d: %v vs %v", i, values[i], original[i])
		}
	}
}

// BenchmarkCorrelateRefineWindow measures a typical PCM refine: a 3 s signature in a 4.5 s window at 11025 Hz.
func BenchmarkCorrelateRefineWindow(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	signal := make([]float64, 49613)
	pattern := make([]float64, 33075)
	for i := range signal {
		signal[i] = rng.NormFloat64()
	}
	for i := range pattern {
		pattern[i] = rng.NormFloat64()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CorrelateNormalized(signal, pattern)
	}
}

// BenchmarkCorrelateRefineWindowDirect is the same workload on the direct O(n*m) loop, for comparison.
func BenchmarkCorrelateRefineWindowDirect(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	signal := make([]float64, 49613)
	pattern := make([]float64, 33075)
	for i := range signal {
		signal[i] = rng.NormFloat64()
	}
	patEnergy := 0.0
	for i := range pattern {
		pattern[i] = rng.NormFloat64()
		patEnergy += pattern[i] * pattern[i]
	}
	patEnergy = math.Sqrt(patEnergy)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		correlateNormalizedDirect(signal, pattern, patEnergy)
	}
}
