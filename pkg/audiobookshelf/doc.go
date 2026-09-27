// Package audiobookshelf exports finalized Podsync episode media into an
// Audiobookshelf podcast library by hardlinking.
//
// Hardlinks are local-filesystem only: the Podsync source file and the
// Audiobookshelf destination directory must live on the same device. The
// exporter never copies media as a fallback and never overwrites an existing
// destination that is not already the same inode as the source.
package audiobookshelf
