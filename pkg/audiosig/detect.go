package audiosig

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Detect runs coarse and refine matching to locate signature in input.
// Inputs:
// - ctx: context for cancellation.
// - inputPath: input media path.
// - signaturePath: signature clip path.
// - cfg: detection configuration.
// Outputs:
// - result: detection result with timestamps and confidence.
// - err: error if decoding or matching fails.
// Example usage:
//
//	res, err := Detect(ctx, "input.mp3", "sig.mp3", cfg)
//
// Notes: Decodes the full input once. To match several signatures against the same input,
// use AnalyzeInput and call Detect/DetectAll on the analysis instead.
func Detect(ctx context.Context, inputPath string, signaturePath string, cfg Config) (Result, error) {
	start := time.Now()
	analysis, err := AnalyzeInput(ctx, inputPath, cfg)
	if err != nil {
		return Result{}, err
	}
	result, err := analysis.Detect(ctx, signaturePath)
	if err != nil {
		return Result{}, err
	}
	result.Runtime = time.Since(start)
	return result, nil
}

// InputAnalysis holds the coarse envelope of an input so that any number of signatures can be
// matched against it with a single full decode.
type InputAnalysis struct {
	path     string
	cfg      Config
	envelope []float64
	duration time.Duration
}

// AnalyzeInput decodes the full input once and computes its coarse envelope.
// Inputs: ctx, inputPath, cfg.
// Outputs: reusable analysis, error.
// Example usage:
//
//	analysis, err := AnalyzeInput(ctx, "episode.mp3", cfg)
//	intro, err := analysis.Detect(ctx, "intro.wav")
//	breaks, err := analysis.DetectAll(ctx, "ad_break.wav", 10)
//
// Notes: Refine passes still decode small windows of the input on demand.
func AnalyzeInput(ctx context.Context, inputPath string, cfg Config) (*InputAnalysis, error) {
	if err := EnsureFFmpegAvailable(ctx); err != nil {
		return nil, err
	}
	cfg = cfg.withDefaults()
	envelope, duration, err := inputEnvelope(ctx, inputPath, cfg)
	if err != nil {
		return nil, err
	}
	return &InputAnalysis{path: inputPath, cfg: cfg, envelope: envelope, duration: duration}, nil
}

// Duration returns the decoded input duration.
func (a *InputAnalysis) Duration() time.Duration {
	return a.duration
}

// Detect finds the single strongest occurrence of the signature.
// Notes: Only the best coarse candidate is refined; MatchFound is false when it does not pass
// the configured thresholds.
func (a *InputAnalysis) Detect(ctx context.Context, signaturePath string) (Result, error) {
	start := time.Now()
	sig, scores, err := a.correlate(ctx, signaturePath)
	if err != nil {
		return Result{}, err
	}
	peaks := TopKPeaks(scores, a.cfg.TopK)
	if len(peaks) == 0 {
		return Result{SignatureFingerprint: sig.fingerprint, InputDuration: a.duration, Runtime: time.Since(start)}, nil
	}
	result, err := a.refineCandidate(ctx, sig, peaks[0])
	if err != nil {
		return Result{}, err
	}
	result.Runtime = time.Since(start)
	return result, nil
}

// DetectAll finds up to maxMatches non-overlapping occurrences of the signature, in time order.
// Inputs: ctx, signaturePath, maxMatches (values below 1 are treated as 1).
// Outputs: matched results (only results with MatchFound), error.
// Notes: Coarse candidates are taken strongest first, at least one signature length apart, and
// each is confirmed with the same refine pass and thresholds as Detect. The search stops after
// TopK consecutive candidates fail to confirm.
func (a *InputAnalysis) DetectAll(ctx context.Context, signaturePath string, maxMatches int) ([]Result, error) {
	start := time.Now()
	if maxMatches < 1 {
		maxMatches = 1
	}
	sig, scores, err := a.correlate(ctx, signaturePath)
	if err != nil {
		return nil, err
	}

	candidates := SeparatedPeaks(scores, len(sig.env), maxMatches+a.cfg.TopK)
	var (
		results  []Result
		failures int
	)
	for _, candidate := range candidates {
		if len(results) >= maxMatches || failures >= a.cfg.TopK {
			break
		}
		result, err := a.refineCandidate(ctx, sig, candidate)
		if err != nil {
			return nil, err
		}
		// Refine windows are wider than the separation, so two candidates can settle on one occurrence.
		if !result.MatchFound || overlapsAny(results, result) {
			failures++
			continue
		}
		failures = 0
		results = append(results, result)
	}

	sort.Slice(results, func(i, j int) bool { return results[i].SignatureStart < results[j].SignatureStart })
	runtime := time.Since(start)
	for i := range results {
		results[i].Runtime = runtime
	}
	return results, nil
}

func overlapsAny(results []Result, candidate Result) bool {
	for _, existing := range results {
		if candidate.SignatureStart < existing.SignatureEnd && existing.SignatureStart < candidate.SignatureEnd {
			return true
		}
	}
	return false
}

// correlate loads the signature and scores it against the input's coarse envelope.
func (a *InputAnalysis) correlate(ctx context.Context, signaturePath string) (*signature, []float64, error) {
	sig, err := loadSignature(ctx, signaturePath, a.cfg)
	if err != nil {
		return nil, nil, err
	}
	if len(a.envelope) < len(sig.env) {
		return nil, nil, fmt.Errorf("input shorter than signature")
	}
	return sig, CorrelateNormalized(a.envelope, sig.env), nil
}

// refineCandidate refines one coarse candidate and applies the match decision.
func (a *InputAnalysis) refineCandidate(ctx context.Context, sig *signature, candidate Peak) (Result, error) {
	cfg := a.cfg
	coarseOffsetSec := float64(candidate.Offset) / float64(cfg.EnvFPS)

	margin := cfg.Margin
	if margin <= 0 {
		margin = 15 * time.Second
	}
	windowStart := time.Duration(coarseOffsetSec*float64(time.Second)) - margin
	if windowStart < 0 {
		windowStart = 0
	}
	windowDur := margin + sig.dur + margin + cfg.ExtraPad
	refineOffset, refineScore, refineRatio, err := a.refineMatch(ctx, sig, windowStart, windowDur)
	if err != nil {
		return Result{}, err
	}

	matchFound := MatchDecision(refineScore, refineRatio, cfg.MinScore, cfg.MinPeakRatio)
	result := Result{
		SignatureFingerprint: sig.fingerprint,
		InputDuration:        a.duration,
		MatchFound:           matchFound,
		ConfidenceScore:      refineScore,
		PeakRatio:            refineRatio,
		CoarseScore:          candidate.Score,
		CoarseOffset:         time.Duration(coarseOffsetSec * float64(time.Second)),
		RefinedOffset:        refineOffset,
	}
	if matchFound {
		result.SignatureStart = refineOffset
		result.SignatureEnd = refineOffset + sig.dur
		result.SplitAt = result.SignatureEnd
	}
	return result, nil
}

func (c Config) withDefaults() Config {
	if c.RefineEnvFPS == 0 {
		c.RefineEnvFPS = c.EnvFPS
	}
	if c.TopK <= 0 {
		c.TopK = 5
	}
	return c
}

// signature caches the decoded forms of a signature clip for one detection run.
type signature struct {
	path        string
	coarsePCM   []int16
	env         []float64
	refineEnv   []float64
	dur         time.Duration
	fingerprint string
	refinePCM   []int16
}

func loadSignature(ctx context.Context, signaturePath string, cfg Config) (*signature, error) {
	pcm, env, dur, fingerprint, err := signatureEnvelope(ctx, signaturePath, cfg)
	if err != nil {
		return nil, err
	}
	refineEnv := ComputeEnvelope(pcm, EnvelopeConfig{SampleRate: cfg.CoarseSampleRate, EnvFPS: cfg.RefineEnvFPS}).Values
	return &signature{path: signaturePath, coarsePCM: pcm, env: env, refineEnv: refineEnv, dur: dur, fingerprint: fingerprint}, nil
}

// refineSamples decodes the signature at the refine sample rate once and caches it.
func (s *signature) refineSamples(ctx context.Context, cfg Config) ([]int16, error) {
	if s.refinePCM != nil {
		return s.refinePCM, nil
	}
	rc, stderr, cmd, err := FFmpegDecoder(ctx, s.path, cfg.RefineSampleRate, 0, 0)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	pcm, err := ReadAllPCM(rc)
	if err != nil {
		return nil, err
	}
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffmpeg refine signature decode: %w (%s)", err, stderr.String())
	}
	s.refinePCM = pcm
	return pcm, nil
}

// signatureEnvelope decodes the signature and computes coarse envelope and fingerprint.
// Inputs: ctx, signaturePath, cfg.
// Outputs: PCM samples, envelope values, duration, fingerprint, error.
// Example usage:
//
//	pcm, env, dur, fp, err := signatureEnvelope(ctx, sigPath, cfg)
//
// Notes: Signature is fully decoded at coarse sample rate.
func signatureEnvelope(ctx context.Context, signaturePath string, cfg Config) ([]int16, []float64, time.Duration, string, error) {
	rc, stderr, cmd, err := FFmpegDecoder(ctx, signaturePath, cfg.CoarseSampleRate, 0, 0)
	if err != nil {
		return nil, nil, 0, "", err
	}
	defer rc.Close()
	pcm, err := ReadAllPCM(rc)
	if err != nil {
		return nil, nil, 0, "", err
	}
	if err := cmd.Wait(); err != nil {
		return nil, nil, 0, "", fmt.Errorf("ffmpeg signature decode: %w (%s)", err, stderr.String())
	}
	stats := ComputeEnvelope(pcm, EnvelopeConfig{SampleRate: cfg.CoarseSampleRate, EnvFPS: cfg.EnvFPS})
	fingerprint := EnvelopeFingerprint(stats.Values, EnvelopeConfig{SampleRate: cfg.CoarseSampleRate, EnvFPS: cfg.EnvFPS}, stats.FrameSize)
	return pcm, stats.Values, time.Duration(stats.DurationSeconds * float64(time.Second)), fingerprint, nil
}

// inputEnvelope streams the input to compute coarse envelope.
// Inputs: ctx, inputPath, cfg.
// Outputs: envelope values, input duration, error.
// Example usage:
//
//	env, dur, err := inputEnvelope(ctx, inputPath, cfg)
//
// Notes: Uses streaming envelope for memory efficiency.
func inputEnvelope(ctx context.Context, inputPath string, cfg Config) ([]float64, time.Duration, error) {
	rc, stderr, cmd, err := FFmpegDecoder(ctx, inputPath, cfg.CoarseSampleRate, 0, 0)
	if err != nil {
		return nil, 0, err
	}
	defer rc.Close()
	stats, err := EnvelopeStream(rc, EnvelopeConfig{SampleRate: cfg.CoarseSampleRate, EnvFPS: cfg.EnvFPS})
	if err != nil {
		return nil, 0, err
	}
	if err := cmd.Wait(); err != nil {
		return nil, 0, fmt.Errorf("ffmpeg input decode: %w (%s)", err, stderr.String())
	}
	return stats.Values, time.Duration(stats.DurationSeconds * float64(time.Second)), nil
}

// refineMatch performs refined envelope match and PCM-level refinement in a window.
// Inputs:
// - ctx, sig: cached signature.
// - windowStart/windowDur: decode window.
// Outputs:
// - refined offset (absolute), score, ratio, error.
// Example usage:
//
//	offset, score, ratio, err := a.refineMatch(ctx, sig, start, dur)
//
// Notes: Runs envelope refine then PCM refine at higher SR. The peak ratio is local to the window.
func (a *InputAnalysis) refineMatch(ctx context.Context, sig *signature, windowStart time.Duration, windowDur time.Duration) (time.Duration, float64, float64, error) {
	cfg := a.cfg
	// Stage 1: envelope refine in window.
	rc, stderr, cmd, err := FFmpegDecoder(ctx, a.path, cfg.CoarseSampleRate, windowStart, windowDur)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rc.Close()
	windowEnv, err := EnvelopeStream(rc, EnvelopeConfig{SampleRate: cfg.CoarseSampleRate, EnvFPS: cfg.RefineEnvFPS})
	if err != nil {
		return 0, 0, 0, err
	}
	if err := cmd.Wait(); err != nil {
		return 0, 0, 0, fmt.Errorf("ffmpeg refine decode: %w (%s)", err, stderr.String())
	}
	refineScores := CorrelateNormalized(windowEnv.Values, sig.refineEnv)
	refinePeaks := TopKPeaks(refineScores, cfg.TopK)
	if len(refinePeaks) == 0 {
		return 0, 0, 0, nil
	}
	bestRefine := refinePeaks[0]
	refineRatio := BestPeakRatio(refinePeaks)
	refineOffsetSec := float64(bestRefine.Offset) / float64(cfg.RefineEnvFPS)
	refineOffset := windowStart + time.Duration(refineOffsetSec*float64(time.Second))

	// Stage 2: PCM refine around best offset at higher SR.
	finalMargin := cfg.FinalMargin
	if finalMargin <= 0 {
		finalMargin = 750 * time.Millisecond
	}
	finalStart := refineOffset - finalMargin
	if finalStart < 0 {
		finalStart = 0
	}
	finalDur := finalMargin + sig.dur + finalMargin

	finalScore, finalOffset, err := a.refinePCM(ctx, sig, finalStart, finalDur)
	if err != nil {
		return 0, 0, 0, err
	}
	return finalOffset, finalScore, refineRatio, nil
}

// refinePCM performs PCM-level normalized correlation at higher SR in a window.
// Inputs:
// - ctx, sig, windowStart, windowDur.
// Outputs: best score and absolute offset.
// Example usage:
//
//	score, offset, err := a.refinePCM(ctx, sig, start, dur)
//
// Notes: Decodes the window at refine sample rate; the signature decode is cached.
func (a *InputAnalysis) refinePCM(ctx context.Context, sig *signature, windowStart time.Duration, windowDur time.Duration) (float64, time.Duration, error) {
	cfg := a.cfg
	sigPCM, err := sig.refineSamples(ctx, cfg)
	if err != nil {
		return 0, 0, err
	}

	// Decode window at refine SR.
	winRC, winStderr, winCmd, err := FFmpegDecoder(ctx, a.path, cfg.RefineSampleRate, windowStart, windowDur)
	if err != nil {
		return 0, 0, err
	}
	defer winRC.Close()
	winPCM, err := ReadAllPCM(winRC)
	if err != nil {
		return 0, 0, err
	}
	if err := winCmd.Wait(); err != nil {
		return 0, 0, fmt.Errorf("ffmpeg refine window decode: %w (%s)", err, winStderr.String())
	}

	if len(winPCM) < len(sigPCM) {
		return 0, 0, fmt.Errorf("refine window shorter than signature")
	}

	winF := normalizePCM(winPCM)
	sigF := normalizePCM(sigPCM)
	scores := CorrelateNormalized(winF, sigF)
	peaks := TopKPeaks(scores, 2)
	if len(peaks) == 0 {
		return 0, 0, nil
	}
	best := peaks[0]
	bestOffsetSec := float64(best.Offset) / float64(cfg.RefineSampleRate)
	bestOffset := windowStart + time.Duration(bestOffsetSec*float64(time.Second))
	return best.Score, bestOffset, nil
}

// normalizePCM converts int16 PCM to float64 and normalizes to zero mean/unit variance.
// Inputs: pcm samples.
// Outputs: normalized float64 slice.
// Example usage:
//
//	vals := normalizePCM(samples)
//
// Notes: Applies mean subtraction and variance normalization.
func normalizePCM(pcm []int16) []float64 {
	values := make([]float64, len(pcm))
	for i, v := range pcm {
		values[i] = float64(v)
	}
	normalizeInPlace(values)
	return values
}
