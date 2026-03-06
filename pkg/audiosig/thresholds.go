package audiosig

// MatchDecision evaluates whether a match is valid.
// Inputs:
// - score: best correlation score.
// - ratio: best/second peak ratio.
// - minScore: minimum absolute score threshold.
// - minRatio: minimum ratio threshold.
// Outputs:
// - ok: true if match is valid.
// Example usage:
//
//	ok := MatchDecision(score, ratio, 0.6, 1.2)
//
// Notes: Returns false if score or ratio is below thresholds.
func MatchDecision(score float64, ratio float64, minScore float64, minRatio float64) bool {
	return score >= minScore && ratio >= minRatio
}
