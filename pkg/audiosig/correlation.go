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
// Notes: Short patterns use a direct O(n*m) loop; long patterns (see fftMinPatternLength) use
// FFT in O(n log n) with equivalent results.
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
	if len(pattern) >= fftMinPatternLength {
		return correlateNormalizedFFT(signal, pattern, patEnergy)
	}
	return correlateNormalizedDirect(signal, pattern, patEnergy)
}

func correlateNormalizedDirect(signal []float64, pattern []float64, patEnergy float64) []float64 {
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

// SeparatedPeaks returns up to limit peaks, strongest first, with every pair of chosen peaks at
// least minSeparation offsets apart (greedy non-maximum suppression).
// Inputs:
// - scores: per-offset scores.
// - minSeparation: minimum distance between chosen offsets (values below 1 are treated as 1).
// - limit: maximum peaks to return.
// Outputs:
// - peaks sorted descending by score.
// Example usage:
//
//	candidates := SeparatedPeaks(scores, len(signatureEnvelope), 10)
//
// Notes: Used to find repeated occurrences, where TopKPeaks would return neighbors of one peak.
func SeparatedPeaks(scores []float64, minSeparation int, limit int) []Peak {
	if limit <= 0 || len(scores) == 0 {
		return []Peak{}
	}
	if minSeparation < 1 {
		minSeparation = 1
	}
	order := make([]int, len(scores))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })

	chosen := make([]Peak, 0, limit)
	for _, offset := range order {
		if len(chosen) >= limit {
			break
		}
		separated := true
		for _, peak := range chosen {
			distance := offset - peak.Offset
			if distance < 0 {
				distance = -distance
			}
			if distance < minSeparation {
				separated = false
				break
			}
		}
		if separated {
			chosen = append(chosen, Peak{Offset: offset, Score: scores[offset]})
		}
	}
	return chosen
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
