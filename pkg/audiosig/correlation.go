package audiosig

import (
	"math"
	"sort"
)

// Peak captures a correlation peak.
// Inputs: none (struct definition).
// Outputs:
// - Offset: lag index (in frames).
// - Score: normalized score at this offset.
// Example usage:
//
//	peaks := TopKPeaks(corr, 5)
//
// Notes: Offset corresponds to signature start in input frames.
type Peak struct {
	Offset int
	Score  float64
}

// CorrelateNormalized computes normalized cross-correlation for signal and pattern.
// Inputs:
// - signal: longer vector.
// - pattern: shorter vector.
// Outputs:
// - scores: per-offset normalized correlation scores.
// Example usage:
//
//	scores := CorrelateNormalized(signal, pattern)
//
// Notes: Uses O(n*m) and is intended for envelope-level sizes.
func CorrelateNormalized(signal []float64, pattern []float64) []float64 {
	if len(signal) == 0 || len(pattern) == 0 || len(signal) < len(pattern) {
		return []float64{}
	}
	patEnergy := 0.0
	for _, v := range pattern {
		patEnergy += v * v
	}
	if patEnergy < 1e-12 {
		return []float64{}
	}
	patEnergy = math.Sqrt(patEnergy)
	maxOffset := len(signal) - len(pattern)
	scores := make([]float64, maxOffset+1)
	for offset := 0; offset <= maxOffset; offset++ {
		sum := 0.0
		sigEnergy := 0.0
		for i, v := range pattern {
			s := signal[offset+i]
			sum += s * v
			sigEnergy += s * s
		}
		denom := patEnergy * math.Sqrt(sigEnergy)
		if denom > 1e-12 {
			scores[offset] = sum / denom
		}
	}
	return scores
}

// TopKPeaks returns the top K peaks sorted by score descending.
// Inputs:
// - scores: correlation scores.
// - k: number of peaks to return.
// Outputs: slice of peaks.
// Example usage:
//
//	peaks := TopKPeaks(scores, 5)
//
// Notes: Does not enforce peak distance; caller may filter if needed.
func TopKPeaks(scores []float64, k int) []Peak {
	if k <= 0 || len(scores) == 0 {
		return []Peak{}
	}
	peaks := make([]Peak, 0, len(scores))
	for i, v := range scores {
		peaks = append(peaks, Peak{Offset: i, Score: v})
	}
	sort.Slice(peaks, func(i, j int) bool { return peaks[i].Score > peaks[j].Score })
	if len(peaks) > k {
		return peaks[:k]
	}
	return peaks
}

// BestPeakRatio computes best/second-best ratio for a peak list.
// Inputs: peaks sorted descending by score.
// Outputs:
// - ratio: best/second score ratio.
// Example usage:
//
//	ratio := BestPeakRatio(peaks)
//
// Notes: Returns +Inf if only one peak exists and best > 0.
func BestPeakRatio(peaks []Peak) float64 {
	if len(peaks) == 0 {
		return 0
	}
	if len(peaks) == 1 {
		if peaks[0].Score <= 0 {
			return 0
		}
		return math.Inf(1)
	}
	if peaks[1].Score == 0 {
		return math.Inf(1)
	}
	return peaks[0].Score / peaks[1].Score
}
