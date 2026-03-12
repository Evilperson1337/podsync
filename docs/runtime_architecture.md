# Runtime Architecture

This document summarizes the current runtime model after the reliability, scheduling, publication, and traceability improvements.

## Update lifecycle

Episodes now move through explicit persisted states in [`pkg/model/feed.go`](../pkg/model/feed.go):

- `new`
- `planned`
- `downloading`
- `processing`
- `stored`
- `published`
- `error`
- `cleaned`

The update pipeline in [`services/update/updater.go`](../services/update/updater.go) persists these transitions around expensive work so interrupted runs are easier to diagnose and repair.

## Reconciliation

At the start of an update run, [`(*Manager).reconcileFeedState()`](../services/update/updater.go) repairs interrupted transient states such as `planned`, `downloading`, `processing`, and `stored` into retryable `error` state with persisted failure metadata.

## Scheduling and execution

Feed execution is coordinated by [`services/update/scheduler.go`](../services/update/scheduler.go).

Current behavior:

- bounded global concurrency
- per-feed mutual exclusion
- provider-scoped throttling for selected providers such as Rumble
- durable per-run `execution_id` propagation for log correlation

The runtime entrypoint in [`cmd/podsync/main.go`](../cmd/podsync/main.go) wires the scheduler, cron triggers, OPML publisher, and shutdown flush behavior together.

## Publication model

Publication concerns are owned by [`services/update/publisher.go`](../services/update/publisher.go).

That service is responsible for:

- rendering feed XML
- rendering OPML
- updating persisted publication summaries

OPML rebuild requests are debounced through [`services/update/opml_publisher.go`](../services/update/opml_publisher.go).

## Storage publish semantics

Low-level storage is defined in [`pkg/fs/storage.go`](../pkg/fs/storage.go).

Publication now uses native publish sessions via:

- [`BeginPublish()`](../pkg/fs/storage.go)
- [`Commit()`](../pkg/fs/storage.go)
- [`Abort()`](../pkg/fs/storage.go)

The shared helper in [`pkg/fs/publish.go`](../pkg/fs/publish.go) stages content, performs lightweight validation such as minimum size checks, and commits through the backend session.

Backend notes:

- [`pkg/fs/local.go`](../pkg/fs/local.go) uses staged local files and atomic rename.
- [`pkg/fs/s3.go`](../pkg/fs/s3.go) stages locally, uploads to a temporary key, then publishes by copy to the final object key.

## Health and publication summaries

Derived runtime summaries are persisted via [`pkg/model/summary.go`](../pkg/model/summary.go) and stored through [`pkg/db/storage.go`](../pkg/db/storage.go) / [`pkg/db/badger.go`](../pkg/db/badger.go).

These summaries currently cover:

- cached health state for `/health`
- publication counts and last publication timestamps

## Tracing and auditability

Tracing helpers live in [`services/update/trace.go`](../services/update/trace.go).

Important tracing features:

- per-run `execution_id`
- scheduler start/finish/failure logs
- summary refresh audit logs
- trim-path resource logs

## Builder contract

Provider builders under [`pkg/builder`](../pkg/builder) normalize provider-specific inputs into a shared [`model.Feed`](../pkg/model/feed.go) shape.

Shared initialization lives in [`pkg/builder/common.go`](../pkg/builder/common.go), and contract-oriented coverage exists in [`pkg/builder/common_test.go`](../pkg/builder/common_test.go) and [`pkg/builder/contract_test.go`](../pkg/builder/contract_test.go).
