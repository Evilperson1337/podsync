package audiosig

import (
	"bufio"
	"io"
	"math"
)

// EnvelopeStream computes an envelope by streaming PCM samples without buffering full PCM.
// Inputs:
// - r: reader of s16le mono PCM.
// - cfg: envelope configuration.
// Outputs:
// - stats: envelope stats including normalized derivative values.
// Example usage:
//
//	stats, err := EnvelopeStream(rc, EnvelopeConfig{SampleRate: 4000, EnvFPS: 25})
//
// Notes: This is intended for coarse pass to reduce memory.
func EnvelopeStream(r io.Reader, cfg EnvelopeConfig) (EnvelopeStats, error) {
	frameSize := int(math.Round(float64(cfg.SampleRate) / float64(cfg.EnvFPS)))
	if frameSize < 1 {
		frameSize = 1
	}
	reader := bufio.NewReader(r)
	var env []float64
	frameSamples := 0
	frameSum := 0.0
	var totalSamples int
	chunk := make([]byte, 8192)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			for i := 0; i+1 < len(data); i += 2 {
				sample := int16(data[i]) | int16(data[i+1])<<8
				v := float64(sample)
				frameSum += v * v
				frameSamples++
				totalSamples++
				if frameSamples >= frameSize {
					rms := math.Sqrt(frameSum/float64(frameSamples) + 1e-12)
					env = append(env, math.Log1p(rms))
					frameSamples = 0
					frameSum = 0
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return EnvelopeStats{}, err
		}
	}
	// Match ComputeEnvelope behavior: drop incomplete trailing frame.
	_ = frameSamples
	if len(env) < 2 {
		return EnvelopeStats{Values: []float64{}, FrameSize: frameSize, DurationSeconds: float64(totalSamples) / float64(cfg.SampleRate)}, nil
	}
	deriv := make([]float64, len(env)-1)
	for i := 1; i < len(env); i++ {
		deriv[i-1] = env[i] - env[i-1]
	}
	normalizeInPlace(deriv)
	return EnvelopeStats{
		Values:          deriv,
		FrameSize:       frameSize,
		DurationSeconds: float64(totalSamples) / float64(cfg.SampleRate),
	}, nil
}
