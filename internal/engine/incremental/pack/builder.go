package pack

import (
	"errors"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
)

// Builder assembles one pack file from pre-encrypted blobs. The repository
// layer owns the 16 MiB flush decision; the builder just accumulates.
type Builder struct {
	data  []byte
	blobs []Blob
}

func NewBuilder() *Builder { return &Builder{} }

// Add appends one encrypted blob (ciphertext already carries its IV and
// MAC). uncompressedLength is 0 for uncompressed blobs.
func (b *Builder) Add(blobType incremental.BlobType, id incremental.ID, ciphertext []byte, uncompressedLength uint32) {
	b.blobs = append(b.blobs, Blob{
		Type:               blobType,
		ID:                 id,
		Offset:             uint32(len(b.data)),
		Length:             uint32(len(ciphertext)),
		UncompressedLength: uncompressedLength,
	})
	b.data = append(b.data, ciphertext...)
}

// Count returns the number of blobs added.
func (b *Builder) Count() int { return len(b.blobs) }

// Size returns the current encrypted payload size in bytes.
func (b *Builder) Size() int { return len(b.data) }

// Finalize returns the complete pack file: encrypted blobs, the encrypted
// header, and the 4-byte little-endian header length trailer.
func (b *Builder) Finalize(master *crypto.MasterKey) ([]byte, error) {
	if len(b.blobs) == 0 {
		return nil, errors.New("pack: refusing to write an empty pack")
	}
	header, err := buildHeader(b.blobs)
	if err != nil {
		return nil, err
	}
	sealedHeader, err := master.Seal(nil, header)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(b.data)+len(sealedHeader)+trailerSize)
	out = append(out, b.data...)
	out = append(out, sealedHeader...)
	out = append(out,
		byte(len(sealedHeader)),
		byte(len(sealedHeader)>>8),
		byte(len(sealedHeader)>>16),
		byte(len(sealedHeader)>>24),
	)
	return out, nil
}

// buildHeader serializes the decrypted header entries.
func buildHeader(blobs []Blob) ([]byte, error) {
	size := 0
	for _, blob := range blobs {
		size += entryUncompressed
		if blob.Type == incremental.CompressedDataBlob || blob.Type == incremental.CompressedTreeBlob {
			size += 4
		}
	}
	header := make([]byte, 0, size)
	for _, blob := range blobs {
		header = append(header, byte(blob.Type))
		header = append(header, uint32le(blob.Length)...)
		if blob.Type == incremental.CompressedDataBlob || blob.Type == incremental.CompressedTreeBlob {
			header = append(header, uint32le(blob.UncompressedLength)...)
		}
		header = append(header, blob.ID[:]...)
	}
	return header, nil
}

func uint32le(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}
