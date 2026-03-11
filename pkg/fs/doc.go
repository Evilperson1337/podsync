// Package fs owns storage backends and publication layering for feed and media
// artifacts.
//
// Responsibilities:
//   - backend implementations such as local filesystem and S3-compatible storage
//   - low-level create/delete/size semantics exposed by [`Storage`](pkg/fs/storage.go)
//   - staged publish behavior exposed by [`Publisher`](pkg/fs/publish.go)
//
// Ownership boundaries:
//   - callers should prefer [`Publisher`](pkg/fs/publish.go) when they need an
//     explicit stage/validate/commit flow above raw backend writes
//   - higher-level publication policy such as when to build XML/OPML is owned
//     by [`services/update`](services/update)
//
// This package should remain backend-focused rather than absorbing scheduler,
// database, or feed-generation concerns.
package fs
