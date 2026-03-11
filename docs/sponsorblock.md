# SponsorBlock

## Overview

SponsorBlock is a community-maintained API that provides timestamped skip segments for online videos. Podsync can use those segments during media processing so configured feeds automatically remove unwanted sections such as sponsorships, intros, outros, and other supported categories.

SponsorBlock trimming is optional and feed-specific. When it is disabled, existing trim behavior remains unchanged. When it is enabled, SponsorBlock segments are merged into the same trim plan used by signature-based trimming.

## Supported Categories

| Category | Description |
| --- | --- |
| `sponsor` | Paid sponsor segments |
| `intro` | Intro animations or opening segments |
| `outro` | Outro or ending segments |
| `interaction` | Calls to like, subscribe, comment, or engage |
| `selfpromo` | Creator self-promotion |
| `music_offtopic` | Non-music off-topic music sections |
| `preview` | Preview, recap, or teaser sections |
| `filler` | Filler content |

## How to Discover Categories

SponsorBlock segments can be inspected directly through the API:

```text
https://sponsor.ajay.app/api/skipSegments?videoID=<VIDEO_ID>
```

The response is a JSON array. Each element includes a `category` and a `segment` array:

```json
[
  {
    "category": "sponsor",
    "segment": [120.45, 182.91]
  }
]
```

The `segment` array contains start and end timestamps in seconds.

For Rumble feeds, Podsync uses the Rumble video identifier, such as `v76ws1m`, when querying SponsorBlock.

## Configuring SponsorBlock in `config.toml`

Enable SponsorBlock trimming per feed under [`custom`](../pkg/feed/config.go).

Minimal example:

```toml
[feeds.crowder.feed_custom.sponsorblock]
sponsorBlockEnabled = true
sponsorBlockCategories = ["sponsor"]
```

Multiple categories:

```toml
[feeds.crowder.custom]
sponsorBlockEnabled = true
sponsorBlockCategories = ["sponsor", "intro", "outro"]
```

Disabled configuration:

```toml
[feeds.crowder.custom]
sponsorBlockEnabled = false
sponsorBlockCategories = ["sponsor", "intro"]
```

When `enabled = false`, SponsorBlock is ignored for that feed even if categories are present.

## Enable SponsorBlock Trimming for a Feed

1. Identify the video ID used by the provider.
2. Inspect the SponsorBlock API response for that video.
3. Choose the categories you want removed.
4. Add the SponsorBlock block to the target feed in `config.toml`.
5. Run Podsync normally.

Example:

```toml
[feeds.crowder]
url = "https://rumble.com/c/StevenCrowder/livestreams"
format = "audio"

[feeds.crowder.custom]
sponsorBlockEnabled = true
sponsorBlockCategories = ["sponsor", "intro", "outro"]
```

## Behavior Notes

- If no SponsorBlock segments exist for a video, Podsync logs the lookup and continues normally.
- If the SponsorBlock API fails or returns malformed data, Podsync logs the issue and continues without SponsorBlock trimming.
- SponsorBlock categories are filtered per feed using the configured category list.
- SponsorBlock segments are sorted, overlapping ranges are merged, and then combined with existing signature trim operations into a single trim plan before ffmpeg processing begins.
- Existing trim functionality remains active and continues to work when SponsorBlock is not configured.
