package audiosig

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// TrimCommand builds the ffmpeg command for trimming.
// Inputs:
// - inputPath: input media path.
// - outputPath: output media path.
// - splitAt: split timestamp.
// - copy: whether to use stream copy.
// - bitrateKbps: optional bitrate (kbps) for re-encode; 0 to use default.
// Outputs: command string.
// Example usage:
//
//	cmd := TrimCommand("in.mp3", "out.mp3", 30*time.Second, false, 128)
//
// Notes: Uses accurate re-encode unless copy is true.
func TrimCommand(inputPath string, outputPath string, splitAt time.Duration, copy bool, bitrateKbps int) string {
	args := trimArgs(inputPath, outputPath, splitAt, copy, bitrateKbps)
	return "ffmpeg " + strings.Join(args, " ")
}

// RunTrim executes ffmpeg to trim audio from splitAt to end.
// Inputs: ctx, inputPath, outputPath, splitAt, copy, bitrateKbps.
// Outputs: error on failure.
// Example usage:
//
//	err := RunTrim(ctx, "in.mp3", "out.mp3", splitAt, false, 128)
//
// Notes: Re-encodes with libmp3lame unless copy is requested.
func RunTrim(ctx context.Context, inputPath string, outputPath string, splitAt time.Duration, copy bool, bitrateKbps int) error {
	args := trimArgs(inputPath, outputPath, splitAt, copy, bitrateKbps)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg trim failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// RunTrimWithStderr executes ffmpeg to trim audio and returns stderr output.
// Inputs: ctx, inputPath, outputPath, splitAt, copy, bitrateKbps.
// Outputs: stderr string and error (if any).
// Example usage:
//
//	stderr, err := RunTrimWithStderr(ctx, in, out, splitAt, false, 128)
//
// Notes: Use for detailed logging on failures.
func RunTrimWithStderr(ctx context.Context, inputPath string, outputPath string, splitAt time.Duration, copy bool, bitrateKbps int) (string, error) {
	args := trimArgs(inputPath, outputPath, splitAt, copy, bitrateKbps)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(output)), fmt.Errorf("ffmpeg trim failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// trimArgs builds the ffmpeg argument list for trimming.
// Inputs: inputPath, outputPath, splitAt, copy, bitrateKbps.
// Outputs: argument slice.
// Example usage:
//
//	args := trimArgs("in.mp3", "out.mp3", splitAt, false, 128)
//
// Notes: Uses -ss before -i for fast seek, re-encode for accuracy.
func trimArgs(inputPath string, outputPath string, splitAt time.Duration, copy bool, bitrateKbps int) []string {
	args := []string{
		"-y",
		"-v", "error",
		"-nostdin",
		"-ss", formatFFmpegTime(splitAt),
		"-i", inputPath,
	}
	if copy {
		args = append(args, "-c", "copy")
	} else {
		args = append(args, "-c:a", "libmp3lame")
		if bitrateKbps > 0 {
			args = append(args, "-b:a", strconv.Itoa(bitrateKbps)+"k")
		} else {
			args = append(args, "-q:a", "2")
		}
	}
	args = append(args, outputPath)
	return args
}
