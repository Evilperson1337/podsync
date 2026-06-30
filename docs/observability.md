# Observability and Operations

This document summarizes the main runtime observability surfaces available after the architecture improvements.

## Health endpoint

Podsync exposes [`/health`](../services/web/server.go), which reports:

- overall status
- recent failed episode count
- failure categories

Health state is refreshed from persisted summaries and recomputed when necessary.

## Debug metrics

When [`server.debug_endpoints`](../services/web/server.go) is enabled, Podsync exposes [`/debug/vars`](../services/web/server.go).

Important counters include:

- queue depth / active feeds from [`services/update/scheduler.go`](../services/update/scheduler.go)
- feed run success/failure counters from [`services/update/updater.go`](../services/update/updater.go)
- publication XML/OPML counters from [`services/update/updater.go`](../services/update/updater.go)
- reconciled episode counters from [`services/update/updater.go`](../services/update/updater.go)

## Execution tracing

Each scheduled feed run carries an `execution_id` through the scheduler and updater logs via [`services/update/trace.go`](../services/update/trace.go).

Useful fields to correlate:

- `execution_id`
- `feed_id`
- `episode_id`
- `provider`
- `duration`

## Publication summaries

Publication summaries are persisted through [`pkg/model/summary.go`](../pkg/model/summary.go) and include:

- XML build counts
- OPML build counts
- last XML feed ID
- last publication timestamp/type

## Feed run outcomes

Feed-level success/failure metadata is persisted in [`pkg/model/feed.go`](../pkg/model/feed.go):

- `last_success_at`
- `last_failure_at`
- `last_failure`

Every feed run now emits a concise summary log from [`services/update/updater.go`](../services/update/updater.go) with fields such as:

- `source_items_found`
- `new_items_discovered`
- `already_known`
- `downloaded`
- `reused_existing_media`
- `skipped`
- `excluded`
- `failed`
- `generated_feed_items`
- `duration`

In headless mode, [`cmd/podsync/main.go`](../cmd/podsync/main.go) also emits a global `Podsync sync completed` summary that aggregates all processed feeds.

## Item-level reasons

Episode records in [`pkg/model/feed.go`](../pkg/model/feed.go) persist structured decision details for troubleshooting missing episodes:

- `reason_code` gives a stable machine-readable reason such as `already_downloaded`, `missing_media_url`, `duration_below_minimum`, `download_failed`, `media_probe_failed`, or `storage_write_failed`.
- `reason` gives a human-readable explanation.
- `decision_source` identifies whether the decision came from configuration, automatic detection, cache state, or an error.
- `processed_at` records when the decision was made.
- `diagnostics` stores optional non-sensitive details such as configured duration limits, media path, size, or retry attempts.

Normal logs remain summary-focused. Enable debug logging with [`--debug`](../cmd/podsync/main.go) or `[log].debug = true` in configuration to see item-level messages like `Discovered item`, `Already known item`, `Skipped item`, `Excluded item`, `Downloaded item`, `Reused existing item`, and `Failed item` with feed ID, source URL, GUID, title, reason, and diagnostic fields.

Common troubleshooting mapping:

- Missing media URL: the source item was discovered but had no downloadable media URL; inspect provider metadata/API access.
- Excluded by pattern or duration: check the feed's `[filters]` configuration.
- Already downloaded/reused existing media: Podsync found a matching existing media file and reused it.
- Download failed/rate limited: retry metadata and `last_error` are persisted on the episode.
- Storage write failed: the downloader succeeded, but writing the media object failed; check local/S3 storage permissions and free space.

## Trim observability

Signature trimming logs capture:

- whether the source file was reused or materialized
- staged input bytes
- matched rule counts
- output segment sizes

See [`services/update/signature_trim.go`](../services/update/signature_trim.go) and [`services/update/signature_apply.go`](../services/update/signature_apply.go).
