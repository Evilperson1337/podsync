package audiosig

import (
	"context"
	"fmt"
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
// Notes: Uses ffmpeg for decoding and keeps memory low for coarse pass.
func Detect(ctx context.Context, inputPath string, signaturePath string, cfg Config) (Result, error) {
	start := time.Now()
	if err := EnsureFFmpegAvailable(ctx); err != nil {
		return Result{}, err
	}

	if cfg.RefineEnvFPS == 0 {
		cfg.RefineEnvFPS = cfg.EnvFPS
	}
	if cfg.TopK <= 0 {
		cfg.TopK = 5
	}

	// Decode signature for coarse envelope.
	sigCoarsePCM, sigEnv, sigDur, sigFrameSize, sigFingerprint, err := signatureEnvelope(ctx, signaturePath, cfg)
	if err != nil {
		return Result{}, err
	}

	// Stream input for coarse envelope.
	coarseEnv, inputDur, err := inputEnvelope(ctx, inputPath, cfg)
	if err != nil {
		return Result{}, err
	}

	if len(coarseEnv) < len(sigEnv) {
		return Result{}, fmt.Errorf("input shorter than signature")
	}

	coarseScores := CorrelateNormalized(coarseEnv, sigEnv)
	peaks := TopKPeaks(coarseScores, cfg.TopK)
	if len(peaks) == 0 {
		return Result{SignatureFingerprint: sigFingerprint, InputDuration: inputDur, Runtime: time.Since(start)}, nil
	}
	bestCoarse := peaks[0]
	coarseOffsetSec := float64(bestCoarse.Offset) / float64(cfg.EnvFPS)

	// Refine window decoding.
	margin := cfg.Margin
	if margin <= 0 {
		margin = 15 * time.Second
	}
	extraPad := cfg.ExtraPad
	windowStart := time.Duration(coarseOffsetSec*float64(time.Second)) - margin
	if windowStart < 0 {
		windowStart = 0
	}
	windowDur := margin + sigDur + margin + extraPad
	refineOffset, refineScore, refineRatio, err := refineMatch(ctx, inputPath, signaturePath, sigCoarsePCM, sigDur, windowStart, windowDur, cfg)
	if err != nil {
		return Result{}, err
	}

	matchFound := MatchDecision(refineScore, refineRatio, cfg.MinScore, cfg.MinPeakRatio)
	result := Result{
		SignatureFingerprint: sigFingerprint,
		InputDuration:        inputDur,
		MatchFound:           matchFound,
		ConfidenceScore:      refineScore,
		PeakRatio:            refineRatio,
		CoarseScore:          bestCoarse.Score,
		Runtime:              time.Since(start),
		CoarseOffset:         time.Duration(coarseOffsetSec * float64(time.Second)),
		RefinedOffset:        refineOffset,
	}
	if matchFound {
		result.SignatureStart = refineOffset
		result.SignatureEnd = refineOffset + sigDur
		result.SplitAt = result.SignatureEnd
	}
	_ = sigFrameSize
	return result, nil
}

// signatureEnvelope decodes the signature and computes coarse envelope and fingerprint.
// Inputs: ctx, signaturePath, cfg.
// Outputs: PCM samples, envelope values, duration, frame size, fingerprint, error.
// Example usage:
//
//	pcm, env, dur, frame, fp, err := signatureEnvelope(ctx, sigPath, cfg)
//
// Notes: Signature is fully decoded at coarse sample rate.
func signatureEnvelope(ctx context.Context, signaturePath string, cfg Config) ([]int16, []float64, time.Duration, int, string, error) {
	rc, stderr, cmd, err := FFmpegDecoder(ctx, signaturePath, cfg.CoarseSampleRate, 0, 0)
	if err != nil {
		return nil, nil, 0, 0, "", err
	}
	defer rc.Close()
	pcm, err := ReadAllPCM(rc)
	if err != nil {
		return nil, nil, 0, 0, "", err
	}
	if err := cmd.Wait(); err != nil {
		return nil, nil, 0, 0, "", fmt.Errorf("ffmpeg signature decode: %w (%s)", err, stderr.String())
	}
	stats := ComputeEnvelope(pcm, EnvelopeConfig{SampleRate: cfg.CoarseSampleRate, EnvFPS: cfg.EnvFPS})
	fingerprint := EnvelopeFingerprint(stats.Values, EnvelopeConfig{SampleRate: cfg.CoarseSampleRate, EnvFPS: cfg.EnvFPS}, stats.FrameSize)
	return pcm, stats.Values, time.Duration(stats.DurationSeconds * float64(time.Second)), stats.FrameSize, fingerprint, nil
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
// - ctx, inputPath, signaturePath.
// - sigCoarsePCM: signature PCM at coarse SR.
// - sigDur: signature duration.
// - windowStart/windowDur: decode window.
// - cfg: detection config.
// Outputs:
// - refined offset (absolute), score, ratio, error.
// Example usage:
//
//	offset, score, ratio, err := refineMatch(ctx, in, sig, sigPCM, sigDur, start, dur, cfg)
//
// Notes: Runs envelope refine then PCM refine at higher SR.
func refineMatch(ctx context.Context, inputPath string, signaturePath string, sigCoarsePCM []int16, sigDur time.Duration, windowStart time.Duration, windowDur time.Duration, cfg Config) (time.Duration, float64, float64, error) {
	// Stage 1: envelope refine in window.
	rc, stderr, cmd, err := FFmpegDecoder(ctx, inputPath, cfg.CoarseSampleRate, windowStart, windowDur)
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
	// Signature envelope at same settings.
	sigEnvStats := ComputeEnvelope(sigCoarsePCM, EnvelopeConfig{SampleRate: cfg.CoarseSampleRate, EnvFPS: cfg.RefineEnvFPS})
	refineScores := CorrelateNormalized(windowEnv.Values, sigEnvStats.Values)
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
	finalDur := finalMargin + sigDur + finalMargin

	finalScore, finalOffset, err := refinePCM(ctx, inputPath, signaturePath, finalStart, finalDur, cfg)
	if err != nil {
		return 0, 0, 0, err
	}
	return finalOffset, finalScore, refineRatio, nil
}

// refinePCM performs PCM-level normalized correlation at higher SR in a window.
// Inputs:
// - ctx, inputPath, signaturePath, windowStart, windowDur, cfg.
// Outputs: best score and absolute offset.
// Example usage:
//
//	score, offset, err := refinePCM(ctx, in, sig, start, dur, cfg)
//
// Notes: Decodes window and signature at refine sample rate.
func refinePCM(ctx context.Context, inputPath string, signaturePath string, windowStart time.Duration, windowDur time.Duration, cfg Config) (float64, time.Duration, error) {
	// Decode signature at refine SR.
	sigRC, sigStderr, sigCmd, err := FFmpegDecoder(ctx, signaturePath, cfg.RefineSampleRate, 0, 0)
	if err != nil {
		return 0, 0, err
	}
	defer sigRC.Close()
	sigPCM, err := ReadAllPCM(sigRC)
	if err != nil {
		return 0, 0, err
	}
	if err := sigCmd.Wait(); err != nil {
		return 0, 0, fmt.Errorf("ffmpeg refine signature decode: %w (%s)", err, sigStderr.String())
	}

	// Decode window at refine SR.
	winRC, winStderr, winCmd, err := FFmpegDecoder(ctx, inputPath, cfg.RefineSampleRate, windowStart, windowDur)
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
