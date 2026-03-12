// Package builder converts provider-specific inputs into normalized
// [`model.Feed`](pkg/model/feed.go) results consumed by the update pipeline.
//
// Contract intent:
//   - builders own provider URL parsing, remote fetch behavior, and provider
//     metadata translation
//   - builders should return stable episode IDs and a consistently initialized
//     feed model
//   - shared feed initialization belongs in [`newFeedModel()`](pkg/builder/common.go)
//   - orchestration, persistence, publication, and retry policy do not belong
//     in this package
//
// The package therefore defines a normalization boundary between provider
// integrations and the rest of Podsync.
package builder
