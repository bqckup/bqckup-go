// Test-only copy of the pack reader. Move back to production code when
// restore (L2) actually reads pack files.
package pack

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
)

// Parse reads a complete pack file and returns its blob descriptors.
// open decrypts the header (repository master key).
func Parse(data []byte, master *crypto.MasterKey) ([]Blob, error) {
	if len(data) < trailerSize {
		return nil, errors.New("pack: file is too short to contain a header trailer")
	}
	headerLength := int(binary.LittleEndian.Uint32(data[len(data)-trailerSize:]))
	if headerLength < crypto.Extension || headerLength+trailerSize > len(data) {
		return nil, fmt.Errorf("pack: invalid header length %d", headerLength)
	}
	headerCiphertext := data[len(data)-trailerSize-headerLength : len(data)-trailerSize]
	header, err := master.Open(nil, headerCiphertext)
	if err != nil {
		return nil, fmt.Errorf("pack: decrypt header: %w", err)
	}
	blobs, err := parseHeader(header)
	if err != nil {
		return nil, err
	}
	payloadSize := len(data) - headerLength - trailerSize
	if err := checkOffsets(blobs, payloadSize); err != nil {
		return nil, err
	}
	return blobs, nil
}

// parseHeader decodes the decrypted header entry sequence.
func parseHeader(header []byte) ([]Blob, error) {
	if len(header) == 0 {
		return nil, errors.New("pack: empty header (pack with no blobs)")
	}
	var blobs []Blob
	position := 0
	offset := uint32(0)
	for position < len(header) {
		entryType := incremental.BlobType(header[position])
		position++
		compressed := entryType == incremental.CompressedDataBlob || entryType == incremental.CompressedTreeBlob
		if entryType > incremental.CompressedTreeBlob {
			return nil, fmt.Errorf("pack: unknown blob type %d at header offset %d", entryType, position-1)
		}
		if !compressed && len(header)-position < 4+len(incremental.ID{}) {
			return nil, errors.New("pack: truncated header entry")
		}
		if compressed && len(header)-position < 8+len(incremental.ID{}) {
			return nil, errors.New("pack: truncated compressed header entry")
		}
		length := binary.LittleEndian.Uint32(header[position : position+4])
		position += 4
		uncompressedLength := uint32(0)
		if compressed {
			uncompressedLength = binary.LittleEndian.Uint32(header[position : position+4])
			position += 4
		}
		var id incremental.ID
		copy(id[:], header[position:position+len(id)])
		position += len(id)

		blobs = append(blobs, Blob{
			Type:               entryType,
			ID:                 id,
			Offset:             offset,
			Length:             length,
			UncompressedLength: uncompressedLength,
		})
		offset += length
	}
	return blobs, nil
}

// checkOffsets verifies the blob lengths exactly fill the payload.
func checkOffsets(blobs []Blob, payloadSize int) error {
	total := uint32(0)
	for _, blob := range blobs {
		if blob.Length < crypto.Extension {
			return fmt.Errorf("pack: blob %s has impossible length %d", blob.ID, blob.Length)
		}
		total += blob.Length
	}
	if int(total) != payloadSize {
		return fmt.Errorf("pack: blob lengths (%d) do not match payload size (%d)", total, payloadSize)
	}
	return nil
}
