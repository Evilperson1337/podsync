// Package audiosig provides fast signature detection for audio files.
//
// Inputs:
// - Input media path (MP3/WAV/etc. via ffmpeg).
// - Signature clip path.
// - Detection configuration for coarse/refine passes.
//
// Outputs:
// - Signature start/end timestamps and confidence metrics.
// - Deterministic fingerprint of the signature envelope.
//
// Example usage:
//
//	cfg := audiosig.Config{CoarseSampleRate: 4000, RefineSampleRate: 11025, EnvFPS: 25}
//	res, err := audiosig.Detect(ctx, "input.mp3", "sig.mp3", cfg)
//
// Notes:
// - Uses ffmpeg for decoding to keep dependencies small.
// - Coarse pass streams envelope to avoid full PCM buffering.
package audiosig
