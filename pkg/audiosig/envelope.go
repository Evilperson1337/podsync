package audiosig

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
)

// EnvelopeConfig defines envelope extraction parameters.
// Inputs:
// - SampleRate: sample rate of PCM input.
// - EnvFPS: frames per second for envelope.
// Outputs: none (struct definition).
// Example usage:
//
//	cfg := EnvelopeConfig{SampleRate: 4000, EnvFPS: 25}
//
// Notes: FrameSize is derived from SampleRate/EnvFPS.
type EnvelopeConfig struct {
	SampleRate int
	EnvFPS     int
}

// EnvelopeStats describes a computed envelope and metadata.
// Inputs: none (struct definition).
// Outputs:
// - Values: log-derivative envelope vector.
// - FrameSize: samples per envelope frame.
// - DurationSeconds: total seconds represented.
// Example usage:
//
//	stats := ComputeEnvelope(pcm, cfg)
//
// Notes: Values are normalized to zero mean and unit variance.
type EnvelopeStats struct {
	Values          []float64
	FrameSize       int
	DurationSeconds float64
}

// ComputeEnvelope calculates a log RMS envelope and its first difference.
// Inputs:
// - pcm: int16 mono samples.
// - cfg: envelope configuration.
// Outputs:
// - stats: envelope stats including normalized derivative values.
// Example usage:
//
//	stats := ComputeEnvelope(samples, EnvelopeConfig{SampleRate: 4000, EnvFPS: 25})
//
// Notes: Applies log compression and derivative to increase peakiness.
func ComputeEnvelope(pcm []int16, cfg EnvelopeConfig) EnvelopeStats {
	frameSize := int(math.Round(float64(cfg.SampleRate) / float64(cfg.EnvFPS)))
	if frameSize < 1 {
		frameSize = 1
	}
	frames := len(pcm) / frameSize
	if frames < 1 {
		return EnvelopeStats{Values: []float64{}, FrameSize: frameSize, DurationSeconds: 0}
	}
	env := make([]float64, frames)
	for i := 0; i < frames; i++ {
		start := i * frameSize
		end := start + frameSize
		sum := 0.0
		for _, s := range pcm[start:end] {
			v := float64(s)
			sum += v * v
		}
		rms := math.Sqrt(sum/float64(frameSize) + 1e-12)
		env[i] = math.Log1p(rms)
	}
	// First difference.
	deriv := make([]float64, frames-1)
	for i := 1; i < frames; i++ {
		deriv[i-1] = env[i] - env[i-1]
	}
	normalizeInPlace(deriv)
	return EnvelopeStats{
		Values:          deriv,
		FrameSize:       frameSize,
		DurationSeconds: float64(len(pcm)) / float64(cfg.SampleRate),
	}
}

// EnvelopeFingerprints computes a deterministic fingerprint hash for a signature envelope.
// Inputs:
// - env: normalized envelope values.
// - cfg: envelope configuration.
// - frameSize: computed frame size in samples.
// Outputs:
// - hex digest string.
// Example usage:
//
//	fp := EnvelopeFingerprint(env, cfg, frameSize)
//
// Notes: Includes cfg and frame size to keep hash stable across settings.
func EnvelopeFingerprint(env []float64, cfg EnvelopeConfig, frameSize int) string {
	h := sha256.New()
	meta := fmt.Sprintf("sr=%d;fps=%d;frame=%d;len=%d", cfg.SampleRate, cfg.EnvFPS, frameSize, len(env))
	_, _ = h.Write([]byte(meta))
	buf := make([]byte, 8)
	for _, v := range env {
		bits := math.Float64bits(v)
		binary.LittleEndian.PutUint64(buf, bits)
		_, _ = h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// normalizeInPlace standardizes a float slice to zero mean and unit variance.
// Inputs: data slice.
// Outputs: none (in-place normalization).
// Example usage:
//
//	normalizeInPlace(values)
//
// Notes: If variance is too small, values are zeroed.
func normalizeInPlace(values []float64) {
	if len(values) == 0 {
		return
	}
	mean := 0.0
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	varSum := 0.0
	for i, v := range values {
		d := v - mean
		values[i] = d
		varSum += d * d
	}
	variance := varSum / float64(len(values))
	if variance < 1e-12 {
		for i := range values {
			values[i] = 0
		}
		return
	}
	invStd := 1.0 / math.Sqrt(variance)
	for i := range values {
		values[i] *= invStd
	}
}
