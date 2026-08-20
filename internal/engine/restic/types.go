package restic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ID identifies content by the SHA-256 of its plaintext bytes.
// This is how restic deduplicates: equal bytes produce equal IDs.
type ID [32]byte

// Hash returns the ID of plaintext data.
func Hash(data []byte) ID { return sha256.Sum256(data) }

// ParseID parses a 64-character lowercase hex string into an ID.
func ParseID(s string) (ID, error) {
	var id ID
	if len(s) != hex.EncodedLen(len(id)) {
		return id, fmt.Errorf("invalid ID %q: must be 64 hex characters", s)
	}
	if _, err := hex.Decode(id[:], []byte(s)); err != nil {
		return id, fmt.Errorf("invalid ID %q: %w", s, err)
	}
	return id, nil
}

func (id ID) String() string { return hex.EncodeToString(id[:]) }

func (id ID) IsNull() bool { return id == ID{} }

func (id ID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *ID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := ParseID(s)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// BlobType discriminates the four pack entry types.
type BlobType uint8

const (
	DataBlob           BlobType = 0 // uncompressed data blob
	TreeBlob           BlobType = 1 // uncompressed tree blob
	CompressedDataBlob BlobType = 2 // zstd-compressed data blob
	CompressedTreeBlob BlobType = 3 // zstd-compressed tree blob
)

// String returns the JSON spelling restic uses for index entries.
func (t BlobType) String() string {
	switch t {
	case DataBlob, CompressedDataBlob:
		return "data"
	case TreeBlob, CompressedTreeBlob:
		return "tree"
	default:
		return "invalid"
	}
}

// Blob is one stored unit of deduplication: a chunk of file data or a tree.
type Blob struct {
	BlobType           BlobType
	ID                 ID
	Offset             uint32
	Length             uint32
	UncompressedLength uint32
}

// FileType names a repository storage area (mirrors the Backend layout).
type FileType string

const (
	ConfigFile   FileType = "config"
	KeyFileType  FileType = "key"
	LockFile     FileType = "lock"
	SnapshotFile FileType = "snapshot"
	IndexFile    FileType = "index"
	DataFile     FileType = "data"
)

// Handle addresses one stored file.
type Handle struct {
	Type FileType
	Name string // hex storage ID; empty for ConfigFile
}
