package audiosig

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// FFmpegDecoder executes ffmpeg and streams raw PCM from stdout.
// Inputs:
// - ctx: context for cancellation.
// - inputPath: path to input media.
// - sampleRate: output sample rate in Hz.
// - start: optional seek start time.
// - duration: optional decode duration.
// Outputs:
// - rc: reader for s16le mono PCM data.
// - stderr: captured stderr for error reporting.
// - cmd: running exec.Cmd for process control.
// Example usage:
//
//	rc, stderr, cmd, err := FFmpegDecoder(ctx, "in.mp3", 4000, 0, 0)
//
// Notes: Caller must Close the reader and Wait on cmd if needed.
func FFmpegDecoder(ctx context.Context, inputPath string, sampleRate int, start time.Duration, duration time.Duration) (rc io.ReadCloser, stderr *bytes.Buffer, cmd *exec.Cmd, err error) {
	args := []string{
		"-v", "error",
		"-nostdin",
		"-vn", "-sn", "-dn",
	}
	if start > 0 {
		args = append(args, "-ss", formatFFmpegTime(start))
	}
	args = append(args, "-i", inputPath)
	if duration > 0 {
		args = append(args, "-t", formatFFmpegTime(duration))
	}
	args = append(args,
		"-ac", "1",
		"-ar", strconv.Itoa(sampleRate),
		"-f", "s16le",
		"-",
	)

	cmd = exec.CommandContext(ctx, "ffmpeg", args...)
	stderr = &bytes.Buffer{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, stderr, nil, fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, stderr, nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	return stdout, stderr, cmd, nil
}

// EnsureFFmpegAvailable verifies ffmpeg is on PATH.
// Inputs: ctx for cancellation.
// Outputs: error if ffmpeg is missing or invocation fails.
// Example usage:
//
//	if err := EnsureFFmpegAvailable(ctx); err != nil { ... }
//
// Notes: Uses "ffmpeg -version".
func EnsureFFmpegAvailable(ctx context.Context) error {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("ffmpeg not found in PATH")
	}
	cmd := exec.CommandContext(ctx, path, "-version")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg check failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// FFprobeAudioBitrate returns the input audio bitrate in kbps.
// Inputs:
// - ctx: context for cancellation.
// - inputPath: input media path.
// Outputs:
// - kbps: bitrate in kilobits per second.
// Example usage:
//
//	kbps, err := FFprobeAudioBitrate(ctx, "input.mp3")
//
// Notes: Uses ffprobe and returns error if bitrate is unavailable.
func FFprobeAudioBitrate(ctx context.Context, inputPath string) (int, error) {
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0, fmt.Errorf("ffprobe not found in PATH")
	}
	args := []string{
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=bit_rate",
		"-of", "default=nw=1:nk=1",
		inputPath,
	}
	cmd := exec.CommandContext(ctx, path, args...)
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe bitrate failed: %w", err)
	}
	bitrate, err := parseBitrateValue(string(output))
	if err != nil {
		return 0, err
	}
	kbps := int(math.Round(float64(bitrate) / 1000.0))
	if kbps <= 0 {
		return 0, fmt.Errorf("invalid bitrate: %d", kbps)
	}
	return kbps, nil
}

// parseBitrateValue parses a bitrate string into bits per second.
// Inputs: raw ffprobe output string.
// Outputs: bitrate in bits per second.
// Example usage:
//
//	bitrate, err := parseBitrateValue("128000\n")
//
// Notes: Trims whitespace and expects an integer.
func parseBitrateValue(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("empty bitrate output")
	}
	bitrate, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid bitrate output: %q", trimmed)
	}
	return bitrate, nil
}

// ReadAllPCM drains a PCM reader into a int16 slice.
// Inputs:
// - r: reader returning s16le mono PCM.
// Outputs: slice of samples.
// Example usage:
//
//	samples, err := ReadAllPCM(rc)
//
// Notes: Intended for small windows; avoid for full files.
func ReadAllPCM(r io.Reader) ([]int16, error) {
	buf := bufio.NewReader(r)
	var out []int16
	chunk := make([]byte, 8192)
	for {
		n, err := buf.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			for i := 0; i+1 < len(data); i += 2 {
				sample := int16(data[i]) | int16(data[i+1])<<8
				out = append(out, sample)
			}
		}
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
	}
}

// formatFFmpegTime formats a duration for ffmpeg CLI.
// Inputs: duration.
// Outputs: string in seconds with millisecond precision.
// Example usage:
//
//	formatFFmpegTime(1500*time.Millisecond) // "1.500"
//
// Notes: ffmpeg accepts seconds with fractional part.
func formatFFmpegTime(d time.Duration) string {
	seconds := float64(d) / float64(time.Second)
	return fmt.Sprintf("%.3f", seconds)
}
