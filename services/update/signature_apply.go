package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/mxpv/podsync/pkg/audiosig"
)

type timeRange struct {
	start time.Duration
	end   time.Duration
}

type trimOperation struct {
	action string
	range_ timeRange
}

// applyMatchedRules applies all detected rules on the original input in a single pass.
// Inputs: ctx, inputPath, inputDur, matches, logger.
// Outputs: new input path, cleanup func, error.
func (u *Manager) applyMatchedRules(ctx context.Context, inputPath string, inputDur time.Duration, matches []matchedRule, logger log.FieldLogger) (string, func(), error) {
	if inputDur <= 0 {
		inputDur = resultDurationOrZero(ctx, inputPath, logger)
	}
	if inputDur <= 0 {
		logger.Warn("[trim] Input duration unknown; skipping trim")
		return inputPath, func() {}, nil
	}
	keep := buildTrimPlan(inputDur, matches, logger)
	if len(keep) == 0 {
		logger.Warn("[trim] All audio would be removed by trim rules; skipping")
		return inputPath, func() {}, nil
	}
	logger.WithField("keep_ranges", formatRanges(keep)).Debug("[trim] Computed keep ranges")
	if len(keep) == 1 && keep[0].start <= 0 && keep[0].end >= inputDur {
		logger.Info("[trim] No effective trimming needed")
		return inputPath, func() {}, nil
	}
	return u.trimConcatRanges(ctx, inputPath, keep, logger)
}

func buildTrimPlan(inputDur time.Duration, matches []matchedRule, logger log.FieldLogger) []timeRange {
	keep := []timeRange{{start: 0, end: inputDur}}
	removeRanges := make([]timeRange, 0)
	for _, match := range matches {
		op, ok := buildTrimOperation(inputDur, match, logger)
		if !ok {
			continue
		}
		switch op.action {
		case "cut_before":
			keep = intersectRanges(keep, timeRange{start: op.range_.end, end: inputDur})
		case "cut_after":
			keep = intersectRanges(keep, timeRange{start: 0, end: op.range_.start})
		case "remove_segment":
			removeRanges = append(removeRanges, op.range_)
		}
		if len(keep) == 0 {
			return nil
		}
	}
	for _, remove := range mergeRanges(removeRanges) {
		keep = subtractRange(keep, remove)
		if len(keep) == 0 {
			return nil
		}
	}
	return keep
}

func buildTrimOperation(inputDur time.Duration, match matchedRule, logger log.FieldLogger) (trimOperation, bool) {
	rule := match.rule
	result := match.result
	start := result.SignatureStart - time.Duration(rule.PreSeconds*float64(time.Second))
	end := result.SignatureEnd + time.Duration(rule.PostSeconds*float64(time.Second))
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = 0
	}
	if end > inputDur {
		end = inputDur
	}
	if end <= start {
		return trimOperation{}, false
	}
	switch rule.Action {
	case "cut_before", "cut_after", "remove_segment":
		return trimOperation{action: rule.Action, range_: timeRange{start: start, end: end}}, true
	default:
		logger.WithField("action", rule.Action).Debug("[trim] Unknown trim action; skipping")
		return trimOperation{}, false
	}
}

// trimKeepRange keeps audio between [keepStart, keepEnd].
// Inputs: ctx, inputPath, keepStart, keepEnd, logger.
// Outputs: new input path, cleanup func, error.
// Example usage:
//
//	newPath, cleanup, err := u.trimKeepRange(ctx, inputPath, 10*time.Second, 120*time.Second, logger)
//
// Notes: Writes a new temp file.
func (u *Manager) trimKeepRange(ctx context.Context, inputPath string, keepStart time.Duration, keepEnd time.Duration, logger log.FieldLogger) (string, func(), error) {
	if keepEnd < keepStart {
		return inputPath, func() {}, nil
	}
	segmentDur := keepEnd - keepStart
	if segmentDur <= 0 {
		return inputPath, func() {}, nil
	}

	bitrateKbps, err := audiosig.FFprobeAudioBitrate(ctx, inputPath)
	if err != nil {
		logger.WithError(err).Debug("[trim] Bitrate lookup failed; using default")
		bitrateKbps = 0
	}

	trimOut, err := os.CreateTemp("", "podsync-trim-keep-*.mp3")
	if err != nil {
		return inputPath, func() {}, fmt.Errorf("create temp output: %w", err)
	}
	_ = trimOut.Close()
	_ = os.Remove(trimOut.Name())

	args := []string{
		"-y",
		"-v", "error",
		"-nostdin",
		"-ss", formatDuration(keepStart),
		"-t", formatDuration(segmentDur),
		"-i", inputPath,
		"-c:a", "libmp3lame",
	}
	if bitrateKbps > 0 {
		args = append(args, "-b:a", fmt.Sprintf("%dk", bitrateKbps))
	} else {
		args = append(args, "-q:a", "2")
	}
	args = append(args, trimOut.Name())
	cmd := execCommandContext(ctx, "ffmpeg", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		logger.WithField("ffmpeg_stderr", string(output)).Error("[trim] ffmpeg keep-range error")
		return inputPath, func() {}, fmt.Errorf("trim keep failed: %w", err)
	}
	info, err := os.Stat(trimOut.Name())
	if err != nil || info.Size() <= 0 {
		logger.Warn("[trim] Keep-range output empty; skipping trim result")
		_ = os.Remove(trimOut.Name())
		return inputPath, func() {}, nil
	}
	cleanup := func() {
		_ = os.Remove(trimOut.Name())
	}
	logger.WithFields(log.Fields{"output_bytes": info.Size(), "range_start": keepStart, "range_end": keepEnd}).Debug("[trim] Keep-range segment created")
	return trimOut.Name(), cleanup, nil
}

// trimConcatRanges trims each keep range and concatenates them.
// Inputs: ctx, inputPath, keepRanges, logger.
// Outputs: new input path, cleanup func, error.
func (u *Manager) trimConcatRanges(ctx context.Context, inputPath string, keepRanges []timeRange, logger log.FieldLogger) (string, func(), error) {
	var segments []string
	var cleanups []func()
	for _, keep := range keepRanges {
		if keep.end <= keep.start {
			continue
		}
		segmentPath, cleanup, err := u.trimKeepRange(ctx, inputPath, keep.start, keep.end, logger)
		if err != nil {
			for _, fn := range cleanups {
				fn()
			}
			return inputPath, func() {}, err
		}
		segments = append(segments, segmentPath)
		cleanups = append(cleanups, cleanup)
	}
	if len(segments) == 0 {
		logger.Warn("[trim] No keep ranges produced output; skipping trim")
		return inputPath, func() {}, nil
	}
	if len(segments) == 1 {
		return segments[0], cleanups[0], nil
	}
	concatOut, err := os.CreateTemp("", "podsync-trim-merge-*.mp3")
	if err != nil {
		for _, fn := range cleanups {
			fn()
		}
		return inputPath, func() {}, fmt.Errorf("create concat output: %w", err)
	}
	_ = concatOut.Close()
	_ = os.Remove(concatOut.Name())

	listFile, err := os.CreateTemp("", "podsync-trim-merge-list-*.txt")
	if err != nil {
		for _, fn := range cleanups {
			fn()
		}
		return inputPath, func() {}, fmt.Errorf("create concat list: %w", err)
	}
	for _, segment := range segments {
		_, _ = fmt.Fprintf(listFile, "file '%s'\n", segment)
	}
	_ = listFile.Close()

	cmd := execCommandContext(ctx, "ffmpeg",
		"-y",
		"-v", "error",
		"-nostdin",
		"-f", "concat",
		"-safe", "0",
		"-i", listFile.Name(),
		"-c", "copy",
		concatOut.Name(),
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		logger.WithField("ffmpeg_stderr", string(output)).Error("[trim] ffmpeg concat error")
		for _, fn := range cleanups {
			fn()
		}
		_ = os.Remove(listFile.Name())
		return inputPath, func() {}, fmt.Errorf("concat failed: %w", err)
	}
	info, err := os.Stat(concatOut.Name())
	if err != nil || info.Size() <= 0 {
		logger.Warn("[trim] Concat output empty; skipping trim result")
		for _, fn := range cleanups {
			fn()
		}
		_ = os.Remove(listFile.Name())
		_ = os.Remove(concatOut.Name())
		return inputPath, func() {}, nil
	}
	cleanup := func() {
		for _, fn := range cleanups {
			fn()
		}
		_ = os.Remove(listFile.Name())
		_ = os.Remove(concatOut.Name())
	}
	logger.WithFields(log.Fields{"segments": len(segments), "output_bytes": info.Size()}).Info("[trim] Trim completed")
	return concatOut.Name(), cleanup, nil
}

// execCommandContext is a small wrapper for testability.
// Inputs: ctx, name, args.
// Outputs: *exec.Cmd.
// Example usage:
//
//	cmd := execCommandContext(ctx, "ffmpeg", "-version")
//
// Notes: Isolated for potential mocking.
func execCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// resultDurationOrZero returns duration from ffprobe if available.
// Inputs: ctx, inputPath, logger.
// Outputs: duration.
// Example usage:
//
//	dur := resultDurationOrZero(ctx, path, logger)
//
// Notes: Returns 0 on failure.
func resultDurationOrZero(ctx context.Context, inputPath string, logger log.FieldLogger) time.Duration {
	cmd := execCommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1",
		inputPath,
	)
	output, err := cmd.Output()
	if err != nil {
		logger.WithError(err).Debug("[trim] ffprobe duration failed")
		return 0
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		logger.WithError(err).Debug("[trim] parse duration failed")
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// formatDuration formats a duration for ffmpeg CLI.
// Inputs: duration.
// Outputs: string with millisecond precision.
// Example usage:
//
//	formatDuration(1500*time.Millisecond) // "1.500"
//
// Notes: ffmpeg accepts seconds with fractional part.
func formatDuration(d time.Duration) string {
	seconds := float64(d) / float64(time.Second)
	return fmt.Sprintf("%.3f", seconds)
}

func intersectRanges(ranges []timeRange, keep timeRange) []timeRange {
	var out []timeRange
	for _, r := range ranges {
		start := r.start
		if keep.start > start {
			start = keep.start
		}
		end := r.end
		if keep.end < end {
			end = keep.end
		}
		if end > start {
			out = append(out, timeRange{start: start, end: end})
		}
	}
	return out
}

func subtractRange(ranges []timeRange, remove timeRange) []timeRange {
	var out []timeRange
	for _, r := range ranges {
		if remove.end <= r.start || remove.start >= r.end {
			out = append(out, r)
			continue
		}
		if remove.start > r.start {
			out = append(out, timeRange{start: r.start, end: remove.start})
		}
		if remove.end < r.end {
			out = append(out, timeRange{start: remove.end, end: r.end})
		}
	}
	return out
}

func formatRanges(ranges []timeRange) []string {
	formatted := make([]string, 0, len(ranges))
	for _, r := range ranges {
		formatted = append(formatted, fmt.Sprintf("%s-%s", formatDuration(r.start), formatDuration(r.end)))
	}
	return formatted
}

func mergeRanges(ranges []timeRange) []timeRange {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	merged := make([]timeRange, 0, len(ranges))
	current := ranges[0]
	for _, next := range ranges[1:] {
		if next.start <= current.end {
			if next.end > current.end {
				current.end = next.end
			}
			continue
		}
		merged = append(merged, current)
		current = next
	}
	merged = append(merged, current)
	return merged
}
