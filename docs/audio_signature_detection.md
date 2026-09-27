# Audio Signature Detection (audiosplitdetect)

## Overview

This module provides a fast, pure-Go signature detector that searches for a known clip inside a long audio file.
It uses a two-pass pipeline with a coarse envelope scan and a refined PCM match on a small window.

## Podsync Integration (Automatic Trimming)

Signature trimming is built into the standard `podsync` binary and Docker image. No special build is needed. It runs for a feed that has signature rules. Configure them in `config.toml` (recommended), or in a legacy `rules.json` file.

When a downloaded episode is processed, the episode is analyzed once, each rule's signature is searched for, and all matched rules are applied in a single trim before the episode is published. Episodes with no match are published unchanged.

### Rules in config.toml

```toml
[feeds.crowder]
url = "https://www.youtube.com/@example"
format = "audio"

  [[feeds.crowder.signature_rules]]
  file = "intro.wav"
  action = "cut_before"

  [[feeds.crowder.signature_rules]]
  file = "ad_break.wav"
  action = "remove_segment"
  post = 60
  max_matches = 10

  [[feeds.crowder.signature_rules]]
  file = "/signatures/shared/outro.mp3"   # absolute paths are used as-is
  action = "cut_after"
  min_score = 0.7
```

Rules in `config.toml` are validated at startup. An unknown action, an out-of-range value, or a missing or empty signature file stops Podsync with an error that names the feed and rule, so typos are caught immediately.

### Rule fields

| Field | Required | Description |
| --- | --- | --- |
| `file` | yes | Signature audio file. Relative paths resolve against `<signatures_root>/<feed_id>/signatures/`. |
| `action` | yes | `cut_before`, `cut_after`, or `remove_segment` (see below). |
| `pre` / `post` | no | Padding in seconds, applied as described per action. |
| `max_matches` | no | How many occurrences to act on (default `1`, the strongest match only). Set it higher for signatures that repeat, such as a stinger before every ad break with `remove_segment`. |
| `min_score` | no | Minimum confidence score, 0 to 1 (default `0.6`). Raise it if a rule matches the wrong audio. |
| `min_peak_ratio` | no | How much the best match must stand out from the runner-up nearby (default `1.2`). Raise it to reject ambiguous matches. |

Actions:
- `cut_before`: remove everything before `signature_end + post`.
- `cut_after`: remove everything after `signature_start - pre`.
- `remove_segment`: remove `signature_start - pre` through `signature_end + post`.

All matched rules are combined into one trim plan, and overlapping removals are merged.

### Signatures root

`<signatures_root>` is resolved in this order:

1. `[signatures] root_dir` in the config file.
2. The `PODSYNC_SIGNATURES_DIR` environment variable.
3. The local storage `data_dir` (for example `/app/data` in Docker).

With S3 storage there is no default. Set `root_dir` or `PODSYNC_SIGNATURES_DIR`, or use absolute `file` paths.

Example layout using the default location:

```
/app/data/crowder/signatures/intro.wav
/app/data/crowder/signatures/ad_break.wav
```

### Legacy rules.json

If a feed has no `signature_rules` in `config.toml`, Podsync reads `<signatures_root>/<feed_id>/signatures/rules.json` instead. It uses the same fields:

```json
{
  "rules": [
    {"file": "intro.wav", "action": "cut_before", "pre": 0, "post": 0},
    {"file": "segment.wav", "action": "remove_segment", "pre": 5, "post": 10, "max_matches": 10},
    {"file": "outro.wav", "action": "cut_after", "pre": 0, "post": 0}
  ]
}
```

A template is available at [`signatures_rules_template.json`](signatures_rules_template.json).

- `rules.json` is checked at startup, but problems are only logged as warnings. Invalid rules and missing files are skipped at runtime with a warning.
- When a feed has `signature_rules` in `config.toml`, its `rules.json` is ignored, and a warning is logged.
- To migrate, copy each rule into a `[[feeds.<id>.signature_rules]]` block, then delete `rules.json`.

### Limits

- Repeated occurrences (`max_matches` above 1) must be about 20 seconds apart or more. Occurrences closer together weaken each other's confidence score and may be skipped.
- On video feeds, trimming stream-copies the video, so cuts land on the nearest keyframe (usually within a few seconds). Audio-only media is re-encoded with its original codec for accurate cuts. See [Output format](#output-format).
- Use the CLI below to check how a signature scores before choosing `min_score` or `min_peak_ratio`.

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
- Podsync checks for them at startup when any feed has signature rules (in `config.toml` or `rules.json`), when `root_dir` or `PODSYNC_SIGNATURES_DIR` is set, or when SponsorBlock is enabled.

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
- In Podsync, each episode is decoded for the coarse pass once, and every rule's signature is matched against that single analysis (`audiosig.AnalyzeInput`).
- For `max_matches` above 1, candidates are taken strongest first, at least one signature length apart, and each is confirmed with the same refine pass and thresholds. The search stops after `top-k` consecutive candidates fail to confirm.
- The refine pass decodes only small windows for speed.
- Long correlations (the PCM refine) use FFT, and short ones (coarse envelopes) use a direct loop. Both produce the same scores.
- Trimming defaults to re-encode for sample-accurate cuts and uses the input bitrate when possible.
