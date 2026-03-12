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

## Trim observability

Signature trimming logs capture:

- whether the source file was reused or materialized
- staged input bytes
- matched rule counts
- output segment sizes

See [`services/update/signature_trim.go`](../services/update/signature_trim.go) and [`services/update/signature_apply.go`](../services/update/signature_apply.go).
