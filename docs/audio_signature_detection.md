# Audio Signature Detection (audiosplitdetect)

## Overview

This module provides a fast, pure-Go signature detector that searches for a known clip inside a long audio file.
It uses a two-pass pipeline with a coarse envelope scan and a refined PCM match on a small window.

## Podsync Integration (Automatic Trimming)

Podsync checks for signature files in `/app/data/<feed_id>/signatures/` by default when using local storage.
If a signature file is found, the downloaded episode is scanned and trimmed so the final output starts at the signature end.

Example directory:

```
/app/data/crowder/signatures/<signature>.wav
/app/data/ai_news/signatures/<signature>.mp3
```

## Multiple Signatures + Rules (rules.json)

Place `rules.json` in `/app/data/<feed_id>/signatures/`:

```json
{
  "rules": [
    {"file": "intro.wav", "action": "cut_before", "pre": 0, "post": 0},
    {"file": "segment.wav", "action": "remove_segment", "pre": 5, "post": 10},
    {"file": "outro.wav", "action": "cut_after", "pre": 0, "post": 0}
  ]
}
```

Template file is available at [`signatures_rules_template.json`](signatures_rules_template.json).

Actions:
- `cut_before`: remove everything before `signature_start - pre`.
- `cut_after`: remove everything after `signature_start - pre`.
- `remove_segment`: remove `signature_start - pre` through `signature_end + post`.

Rules are applied sequentially in the listed order.

## Requirements

- `ffmpeg` and `ffprobe` available in `PATH`.

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

See detailed Windows examples in [`docs/audio_signature_examples.md`](docs/audio_signature_examples.md).

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
