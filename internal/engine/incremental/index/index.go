// Package index implements the restic v2 index file format and the
// concurrent in-memory master index (verified in
// restic-format-verification.md §2.7): plaintext is a 0x02 version byte
// followed by zstd-compressed JSON; no length trailer; the supersedes field
// was removed upstream and is not written.
package index

import (
	"github.com/bqckup/bqckup-go/internal/engine/incremental"
)

// Blob is one JSON blob entry. Type marshals as "data" or "tree";
// compression is marked by a non-zero UncompressedLength.
type Blob struct {
	ID                 incremental.ID       `json:"id"`
	Type               incremental.BlobType `json:"type"`
	Offset             uint32               `json:"offset"`
	Length             uint32               `json:"length"`
	UncompressedLength uint32               `json:"uncompressed_length,omitempty"`
}

// Pack groups the blobs stored in one pack file.
type Pack struct {
	ID    incremental.ID `json:"id"`
	Blobs []Blob         `json:"blobs"`
}

// Index is one index file's content.
type Index struct {
	Packs []Pack `json:"packs"`
}
