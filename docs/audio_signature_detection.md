# Audio Signature Detection (audiosplitdetect)

## Overview

This module provides a fast, pure-Go signature detector that searches for a known clip inside a long audio file.
It uses a two-pass pipeline with a coarse envelope scan and a refined PCM match on a small window.

## Podsync Integration (Automatic Trimming)

Signature trimming is built into the standard `podsync` binary and Docker image. No special build is needed. It runs for a feed only when that feed has a `rules.json` file:

```
<signatures_root>/<feed_id>/signatures/rules.json
```

Signature audio files placed there without a `rules.json` are ignored.

`<signatures_root>` is resolved in this order:

1. `[signatures] root_dir` in the config file.
2. The `PODSYNC_SIGNATURES_DIR` environment variable.
3. The local storage `data_dir` (for example `/app/data` in Docker).

With S3 storage there is no default, so set `root_dir` or `PODSYNC_SIGNATURES_DIR` explicitly.

Example layout using the default location:

```
/app/data/crowder/signatures/rules.json
/app/data/crowder/signatures/intro.wav
/app/data/crowder/signatures/outro.mp3
```

When a downloaded episode is processed, each rule's signature is searched for, and all matched rules are applied in a single trim before the episode is published. Episodes with no match are published unchanged.

## Multiple Signatures + Rules (rules.json)

Place `rules.json` in `<signatures_root>/<feed_id>/signatures/`:

```json
{
  "rules": [
    {"file": "intro.wav", "action": "cut_before", "pre": 0, "post": 0},
    {"file": "segment.wav", "action": "remove_segment", "pre": 5, "post": 10},
    {"file": "outro.wav", "action": "cut_after", "pre": 0, "post": 0}
  ]
}
```

A template is available at [`signatures_rules_template.json`](signatures_rules_template.json).

Fields:
- `file`: signature audio file name, relative to the same `signatures` directory.
- `action`: one of the actions below.
- `pre` / `post`: padding in seconds, applied as described per action.

Actions:
- `cut_before`: remove everything before `signature_end + post`.
- `cut_after`: remove everything after `signature_start - pre`.
- `remove_segment`: remove `signature_start - pre` through `signature_end + post`.

All matched rules are combined into one trim plan, and overlapping removals are merged.

Current limits of the Podsync integration:
- Each rule matches at most once per episode: the strongest occurrence. A signature that repeats (for example, before every ad break) is only removed once.
- Match thresholds are fixed (`min-score` 0.6, `min-peak-ratio` 1.2) and cannot be set per rule. Use the CLI below to check how a signature scores.
- On video feeds, trimming stream-copies the video, so cuts land on the nearest keyframe (usually within a few seconds). Audio-only media is re-encoded with its original codec for accurate cuts. See [Output format](#output-format).

## Output format

Trimmed episodes keep the container and codecs Podsync publishes for the feed. The same rules apply to SponsorBlock trimming.

| Downloaded media | How segments are written |
| --- | --- |
| Audio (`mp3`, `aac`/`m4a`, `opus`, `vorbis`, `flac`, `alac`, `wav`) | Re-encoded with the same codec, at the source bitrate where it applies. Cuts are sample-accurate. |
| Video (`format = "video"`, or a custom video format) | Video and audio are stream-copied, with no re-encoding. Cuts snap to the nearest keyframe. |
| Other audio codecs | Stream-copied, never converted to another format. |

The chosen encoding is logged as `[trim] Selected trim output encoding`.

## Requirements

- `ffmpeg` and `ffprobe` available in `PATH`. The Docker image includes both.
- Podsync checks for them at startup when any feed has a `rules.json`, when `root_dir` or `PODSYNC_SIGNATURES_DIR` is set, or when SponsorBlock is enabled.

## CLI Usage

```bash
go run ./cmd/audiosplitdetect \
  -in input.mp3 \
  -sig-audio signature.mp3 \
  -coarse-sr 4000 \
  -refine-sr 11025 \
  -env-fps 25 \
  -margin 15 \
  -topk 5 \
  -min-score 0.6 \
  -min-peak-ratio 1.2
```

To trim the input after the detected signature end:

```bash
go run ./cmd/audiosplitdetect \
  -in input.mp3 \
  -sig-audio signature.mp3 \
  -trim \
  -out output.mp3
```

Use stream copy (fast, less accurate):

```bash
go run ./cmd/audiosplitdetect -in input.mp3 -sig-audio signature.mp3 -trim -out output.mp3 -copy
```

## Examples (Windows)

See detailed Windows examples in [`docs/audio_signature_examples.md`](audio_signature_examples.md).

## Output Fields

- `SignatureFingerprint`: SHA-256 of signature envelope + metadata.
- `InputDuration`: HH:MM:SS
- `SignatureStart`: HH:MM:SS.mmm
- `SignatureEnd`: HH:MM:SS.mmm
- `SplitAt`: HH:MM:SS.mmm
- `MatchFound`: true/false
- `ConfidenceScore`: best normalized correlation score
- `PeakRatio`: best/second-best peak ratio
- `TotalRuntime`: total detection time

## Algorithm Summary

### Pass A: Coarse Search

- Decode the full input at low SR (default 4000 Hz), mono s16le.
- Compute RMS energy envelope at ~25 fps.
- Apply log compression and first-difference derivative to sharpen peaks.
- Correlate envelope vectors to obtain top-k candidate offsets.

### Pass B: Refine Search

- Decode a small window around the best coarse offset.
- Recompute envelope to get within ~100ms.
- Decode a smaller PCM window at higher SR (default 11025 Hz).
- Run normalized cross-correlation to produce final offset and score.

### Match Decision

A match is valid when both conditions are met:

- `score >= min-score`
- `peakRatio >= min-peak-ratio`

These thresholds are exposed as CLI flags.

## Notes

- The coarse pass streams the envelope so memory is bounded.
- The refine pass decodes only small windows for speed.
- Trimming defaults to re-encode for sample-accurate cuts and uses the input bitrate when possible.
