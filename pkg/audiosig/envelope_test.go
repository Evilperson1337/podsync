package audiosig

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

// TestComputeEnvelopeConstant verifies normalization on constant signal.
// Inputs: none (test case).
// Outputs: none (assertions).
// Example usage: go test ./...
// Notes: Constant signal should yield near-zero derivative after normalization.
func TestComputeEnvelopeConstant(t *testing.T) {
	pcm := make([]int16, 4000)
	for i := range pcm {
		pcm[i] = 1000
	}
	stats := ComputeEnvelope(pcm, EnvelopeConfig{SampleRate: 4000, EnvFPS: 25})
	if len(stats.Values) == 0 {
		t.Fatalf("expected non-empty envelope")
	}
	maxAbs := 0.0
	for _, v := range stats.Values {
		if math.Abs(v) > maxAbs {
			maxAbs = math.Abs(v)
		}
	}
	if maxAbs > 1e-6 {
		t.Fatalf("expected near-zero derivative, got max=%f", maxAbs)
	}
}

// TestEnvelopeFingerprintStable verifies deterministic fingerprinting.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Same inputs must produce identical hashes.
func TestEnvelopeFingerprintStable(t *testing.T) {
	pcm := make([]int16, 8000)
	for i := range pcm {
		pcm[i] = int16((i % 200) - 100)
	}
	cfg := EnvelopeConfig{SampleRate: 4000, EnvFPS: 25}
	stats := ComputeEnvelope(pcm, cfg)
	fp1 := EnvelopeFingerprint(stats.Values, cfg, stats.FrameSize)
	fp2 := EnvelopeFingerprint(stats.Values, cfg, stats.FrameSize)
	if fp1 != fp2 {
		t.Fatalf("fingerprint mismatch: %s vs %s", fp1, fp2)
	}
}

// TestEnvelopeStreamMatchesBatch verifies streaming and batch envelope match.
// Inputs: none (test case).
// Outputs: none.
// Example usage: go test ./...
// Notes: Streaming should closely match batch results.
func TestEnvelopeStreamMatchesBatch(t *testing.T) {
	pcm := make([]int16, 10000)
	for i := range pcm {
		pcm[i] = int16(2000 * math.Sin(float64(i)/10.0))
	}
	buf := &bytes.Buffer{}
	_ = binary.Write(buf, binary.LittleEndian, pcm)
	cfg := EnvelopeConfig{SampleRate: 4000, EnvFPS: 25}
	streamStats, err := EnvelopeStream(bytes.NewReader(buf.Bytes()), cfg)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	batchStats := ComputeEnvelope(pcm, cfg)
	if len(streamStats.Values) != len(batchStats.Values) {
		t.Fatalf("length mismatch: %d vs %d", len(streamStats.Values), len(batchStats.Values))
	}
	for i := range streamStats.Values {
		if math.Abs(streamStats.Values[i]-batchStats.Values[i]) > 1e-6 {
			t.Fatalf("value mismatch at %d: %f vs %f", i, streamStats.Values[i], batchStats.Values[i])
		}
	}
}

// BenchmarkComputeEnvelope measures batch envelope computation.
// Inputs: benchmark state.
// Outputs: none.
// Example usage: go test -bench=BenchmarkComputeEnvelope ./pkg/audiosig
// Notes: Uses a 10-second synthetic signal.
func BenchmarkComputeEnvelope(b *testing.B) {
	pcm := make([]int16, 4000*10)
	for i := range pcm {
		pcm[i] = int16(3000 * math.Sin(float64(i)/8.0))
	}
	cfg := EnvelopeConfig{SampleRate: 4000, EnvFPS: 25}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeEnvelope(pcm, cfg)
	}
}
