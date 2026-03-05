package audiosig

import "time"

// Config defines detection settings.
// Inputs:
// - CoarseSampleRate: sample rate for coarse pass decoding (Hz).
// - RefineSampleRate: sample rate for refine decoding (Hz).
// - EnvFPS: frames per second for coarse envelope computation.
// - RefineEnvFPS: frames per second for refine envelope computation.
// - Margin: seconds before/after coarse offset for refine window.
// - FinalMargin: seconds around refined offset for final PCM refine.
// - ExtraPad: extra seconds added to refine window duration.
// - TopK: number of coarse peaks to keep.
// - MinScore: minimum normalized correlation score for match.
// - MinPeakRatio: minimum best/second peak ratio for match.
// Outputs: none (struct definition).
// Example usage:
//
//	cfg := audiosig.Config{CoarseSampleRate: 4000, RefineSampleRate: 11025, EnvFPS: 25, Margin: 15 * time.Second}
//
// Notes: Keep sample rates low for coarse pass to reduce runtime.
type Config struct {
	CoarseSampleRate int
	RefineSampleRate int
	EnvFPS           int
	RefineEnvFPS     int
	Margin           time.Duration
	FinalMargin      time.Duration
	ExtraPad         time.Duration
	TopK             int
	MinScore         float64
	MinPeakRatio     float64
}

// Result describes detection outputs.
// Inputs: none (struct definition).
// Outputs:
// - SignatureFingerprint: deterministic hash for the signature.
// - InputDuration: duration of input media.
// - SignatureStart: detected start time of signature in input.
// - SignatureEnd: detected end time of signature in input.
// - SplitAt: recommended split time (same as SignatureEnd).
// - MatchFound: whether a valid match was found.
// - ConfidenceScore: best correlation score at final stage.
// - PeakRatio: best/second peak ratio at final stage.
// - CoarseScore: best coarse score for diagnostics.
// - Runtime: total detection runtime.
// - CoarseOffset: best coarse offset (seconds).
// - RefinedOffset: best refined offset (seconds).
// Example usage:
//
//	res, err := audiosig.Detect(ctx, in, sig, cfg)
//
// Notes: SignatureStart/End/SplitAt are zero when MatchFound is false.
type Result struct {
	SignatureFingerprint string
	InputDuration        time.Duration
	SignatureStart       time.Duration
	SignatureEnd         time.Duration
	SplitAt              time.Duration
	MatchFound           bool
	ConfidenceScore      float64
	PeakRatio            float64
	CoarseScore          float64
	Runtime              time.Duration
	CoarseOffset         time.Duration
	RefinedOffset        time.Duration
}
