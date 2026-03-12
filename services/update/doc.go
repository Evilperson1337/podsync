// Package update owns the feed-update execution pipeline.
//
// Primary responsibilities:
//   - feed refresh and episode planning
//   - lifecycle transitions and failure metadata updates
//   - media acquisition and optional trim processing
//   - publication triggering and reconciliation entry points
//   - scheduling, queueing, execution IDs, and OPML debounce behavior
//
// Ownership boundaries:
//   - publication rendering and summary persistence are delegated to
//     [`PublicationService`](services/update/publisher.go)
//   - XML/OPML content structures are owned by [`pkg/feed`](pkg/feed)
//   - storage backend semantics are owned by [`pkg/fs`](pkg/fs)
//   - provider-specific feed construction is owned by [`pkg/builder`](pkg/builder)
//
// The package is intentionally the orchestration layer, not the place where
// XML formats, storage backend internals, or provider scraping policies should
// accumulate.
package update
