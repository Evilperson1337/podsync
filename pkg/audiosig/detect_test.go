package audiosig

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signatureExpr is a distinctive 3 second clip: an amplitude-modulated tone with a late overtone.
const signatureExpr = "0.8*sin(2*PI*440*t)*abs(sin(2*PI*1.3*t))+0.4*sin(2*PI*1250*t)*gt(t,1.7)"

const signatureDuration = 3 * time.Second

func testConfig() Config {
	return Config{
		CoarseSampleRate: 4000,
		RefineSampleRate: 11025,
		EnvFPS:           25,
		RefineEnvFPS:     25,
		Margin:           15 * time.Second,
		FinalMargin:      750 * time.Millisecond,
		TopK:             5,
		MinScore:         0.6,
		MinPeakRatio:     1.2,
	}
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func runFFmpeg(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"-y", "-v", "error", "-nostdin"}, args...)
	output, err := exec.Command("ffmpeg", full...).CombinedOutput()
	require.NoError(t, err, string(output))
}

// writeSignature renders the signature clip as WAV.
func writeSignature(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "signature.wav")
	runFFmpeg(t, "-f", "lavfi", "-i", fmt.Sprintf("aevalsrc='%s':s=22050:d=%g", signatureExpr, signatureDuration.Seconds()), path)
	return path
}

// writeInput renders total seconds of quiet pink noise with the signature inserted at each offset, as MP3.
func writeInput(t *testing.T, dir string, total time.Duration, offsets ...time.Duration) string {
	t.Helper()
	var (
		inputs []string
		labels []string
		cursor time.Duration
	)
	addNoise := func(d time.Duration, seed int) {
		idx := len(inputs) / 4
		inputs = append(inputs, "-f", "lavfi", "-i", fmt.Sprintf("anoisesrc=c=pink:a=0.05:r=22050:d=%g:seed=%d", d.Seconds(), seed))
		labels = append(labels, fmt.Sprintf("[%d:a]", idx))
	}
	addSignature := func() {
		idx := len(inputs) / 4
		inputs = append(inputs, "-f", "lavfi", "-i", fmt.Sprintf("aevalsrc='%s':s=22050:d=%g", signatureExpr, signatureDuration.Seconds()))
		labels = append(labels, fmt.Sprintf("[%d:a]", idx))
	}
	for i, offset := range offsets {
		addNoise(offset-cursor, 100+i)
		addSignature()
		cursor = offset + signatureDuration
	}
	addNoise(total-cursor, 999)

	path := filepath.Join(dir, "input.mp3")
	args := append(inputs, "-filter_complex", strings.Join(labels, "")+fmt.Sprintf("concat=n=%d:v=0:a=1[out]", len(labels)), "-map", "[out]", "-c:a", "libmp3lame", "-q:a", "4", path)
	runFFmpeg(t, args...)
	return path
}

// mp3Tolerance covers encoder priming delay and envelope frame quantization.
const mp3Tolerance = 150 * time.Millisecond

func TestDetectFindsSignature(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	sig := writeSignature(t, dir)
	input := writeInput(t, dir, 60*time.Second, 25*time.Second)

	result, err := Detect(context.Background(), input, sig, testConfig())
	require.NoError(t, err)
	require.True(t, result.MatchFound, "score=%.3f ratio=%.3f", result.ConfidenceScore, result.PeakRatio)
	assert.InDelta(t, float64(25*time.Second), float64(result.SignatureStart), float64(mp3Tolerance), "start %s", result.SignatureStart)
	assert.InDelta(t, float64(28*time.Second), float64(result.SignatureEnd), float64(mp3Tolerance), "end %s", result.SignatureEnd)
	assert.InDelta(t, float64(60*time.Second), float64(result.InputDuration), float64(time.Second))
	assert.NotEmpty(t, result.SignatureFingerprint)
}

func TestDetectNoSignature(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	sig := writeSignature(t, dir)
	input := writeInput(t, dir, 60*time.Second)

	result, err := Detect(context.Background(), input, sig, testConfig())
	require.NoError(t, err)
	assert.False(t, result.MatchFound, "score=%.3f ratio=%.3f", result.ConfidenceScore, result.PeakRatio)
	assert.Zero(t, result.SignatureStart)
}

func TestDetectAllFindsRepeatedSignature(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	sig := writeSignature(t, dir)
	offsets := []time.Duration{20 * time.Second, 60 * time.Second, 95 * time.Second}
	input := writeInput(t, dir, 130*time.Second, offsets...)

	analysis, err := AnalyzeInput(context.Background(), input, testConfig())
	require.NoError(t, err)

	results, err := analysis.DetectAll(context.Background(), sig, 10)
	require.NoError(t, err)
	require.Len(t, results, len(offsets))
	for i, result := range results {
		assert.True(t, result.MatchFound)
		assert.InDelta(t, float64(offsets[i]), float64(result.SignatureStart), float64(mp3Tolerance), "occurrence %d start %s", i, result.SignatureStart)
		assert.Equal(t, result.SignatureStart+signatureDuration, result.SignatureEnd)
	}

	limited, err := analysis.DetectAll(context.Background(), sig, 2)
	require.NoError(t, err)
	assert.Len(t, limited, 2, "max matches caps the result count")
	assert.Less(t, limited[0].SignatureStart, limited[1].SignatureStart, "results are in time order")

	// Single-match detection on the same analysis still returns one occurrence.
	single, err := analysis.Detect(context.Background(), sig)
	require.NoError(t, err)
	require.True(t, single.MatchFound)
	matchedOne := false
	for _, offset := range offsets {
		if d := single.SignatureStart - offset; d > -mp3Tolerance && d < mp3Tolerance {
			matchedOne = true
		}
	}
	assert.True(t, matchedOne, "single match %s should be one of the occurrences", single.SignatureStart)
}

func TestDetectAllNoSignature(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	sig := writeSignature(t, dir)
	input := writeInput(t, dir, 60*time.Second)

	analysis, err := AnalyzeInput(context.Background(), input, testConfig())
	require.NoError(t, err)
	results, err := analysis.DetectAll(context.Background(), sig, 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestAnalyzeInputReusedForSeveralSignatures(t *testing.T) {
	requireFFmpeg(t)
	dir := t.TempDir()
	sig := writeSignature(t, dir)
	other := filepath.Join(dir, "other.wav")
	runFFmpeg(t, "-f", "lavfi", "-i", "aevalsrc='0.7*sin(2*PI*300*t)*abs(sin(2*PI*0.4*t))':s=22050:d=3", other)
	input := writeInput(t, dir, 60*time.Second, 30*time.Second)

	analysis, err := AnalyzeInput(context.Background(), input, testConfig())
	require.NoError(t, err)
	assert.InDelta(t, float64(60*time.Second), float64(analysis.Duration()), float64(time.Second))

	found, err := analysis.Detect(context.Background(), sig)
	require.NoError(t, err)
	assert.True(t, found.MatchFound)

	missing, err := analysis.Detect(context.Background(), other)
	require.NoError(t, err)
	assert.False(t, missing.MatchFound, "score=%.3f ratio=%.3f", missing.ConfidenceScore, missing.PeakRatio)
}
