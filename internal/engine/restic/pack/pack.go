// Package pack implements the restic pack file format (v2, verified in
// restic-format-verification.md §2.5): encrypted blobs in order, then an
// encrypted header, then a 4-byte little-endian header length.
package pack

import (
	"github.com/bqckup/bqckup-go/internal/engine/restic"
)

// Blob describes one encrypted blob inside a pack.
type Blob struct {
	Type               restic.BlobType
	ID                 restic.ID
	Offset             uint32 // offset of the encrypted blob in the pack
	Length             uint32 // encrypted length, including the 32-byte overhead
	UncompressedLength uint32 // plaintext length; 0 for uncompressed blobs
}

// Header entry sizes: one type byte, lengths as fixed 4-byte little-endian
// values (verified: NOT uvarints), and the 32-byte plaintext ID.
// Compressed entries carry an extra uncompressed_length.
const (
	entryUncompressed = 1 + 4 + len(restic.ID{})
	entryCompressed   = entryUncompressed + 4
	trailerSize       = 4
)
