// Package repository ties the engine together: init, open, blob saving
// with dedup, pack/index flushing. Packs are always flushed BEFORE the
// index that references them, so a crash can never leave an index pointing
// at a missing pack.
package repository

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/crypto"
	"github.com/bqckup/bqckup-go/internal/engine/restic/index"
	"github.com/bqckup/bqckup-go/internal/engine/restic/pack"
	"github.com/bqckup/bqckup-go/internal/engine/restic/tree"
	"github.com/klauspost/compress/zstd"
)

// LoadBlob reads and decrypts one blob by ID through the master index,
// decompressing it when the index says it was compressed.
func (r *Repository) LoadBlob(ctx context.Context, blobType restic.BlobType, id restic.ID) ([]byte, error) {
	return r.loadBlob(ctx, blobType, id)
}

// LoadTree loads and parses one tree blob.
func (r *Repository) LoadTree(ctx context.Context, id restic.ID) (*tree.Tree, error) {
	data, err := r.LoadBlob(ctx, restic.TreeBlob, id)
	if err != nil {
		return nil, fmt.Errorf("repository: load tree %s: %w", id, err)
	}
	t, err := tree.Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("repository: parse tree %s: %w", id, err)
	}
	return t, nil
}

// LoadSnapshot loads one snapshot document by its storage ID.
func (r *Repository) LoadSnapshot(ctx context.Context, id restic.ID) (SnapshotWithID, error) {
	return r.loadSnapshot(ctx, restic.Handle{Type: restic.SnapshotFile, Name: id.String()})
}

// DefaultPackSize is the pack flush threshold (16 MiB), matching restic.
const DefaultPackSize = 16 * 1024 * 1024

// Repository is an opened backup repository.
type Repository struct {
	backend backend.Backend
	master  *crypto.MasterKey
	config  Config
	index   *index.MasterIndex

	// ponytail: one mutex for the whole save path (packer append + flush).
	// Per-type packer locks if flush contention ever shows up in profiling.
	saveMu sync.Mutex

	dataPacker *pack.Builder
	treePacker *pack.Builder
	dataBlobs  []index.Entry
	treeBlobs  []index.Entry
	zstd       *zstd.Encoder
}

func newMasterIndex() *index.MasterIndex { return index.NewMasterIndex() }

// newRepository assembles an opened repository with fresh packers.
func newRepository(b backend.Backend, master *crypto.MasterKey, config Config) (*Repository, error) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("repository: create zstd encoder: %w", err)
	}
	return &Repository{
		backend:    b,
		master:     master,
		config:     config,
		index:      newMasterIndex(),
		dataPacker: pack.NewBuilder(),
		treePacker: pack.NewBuilder(),
		zstd:       encoder,
	}, nil
}

// Config returns the loaded repository config.
func (r *Repository) Config() Config { return r.config }

// MasterKey returns the repository's master key. Lock files are stored
// encrypted with it (restic's SaveUnpacked format), so lock operations
// need it after the repository is opened.
func (r *Repository) MasterKey() *crypto.MasterKey { return r.master }

// MasterIndex exposes the in-memory dedup index.
func (r *Repository) MasterIndex() *index.MasterIndex { return r.index }

// SaveBlob stores plaintext data once and returns its ID. The type is
// DataBlob or TreeBlob; compression and the exact pack entry type are
// decided here (v2: everything non-empty is zstd-compressed, zero-length
// blobs never are — verification notes §2.6).
func (r *Repository) SaveBlob(ctx context.Context, blobType restic.BlobType, data []byte) (restic.ID, error) {
	if err := ctx.Err(); err != nil {
		return restic.ID{}, err
	}
	if blobType != restic.DataBlob && blobType != restic.TreeBlob {
		return restic.ID{}, fmt.Errorf("repository: invalid blob type %d", blobType)
	}
	id := restic.Hash(data)
	if _, exists := r.index.Lookup(blobType, id); exists {
		return id, nil // dedup: already stored
	}

	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	// re-check under the lock: another worker may have stored it meanwhile
	if _, exists := r.index.Lookup(blobType, id); exists {
		return id, nil
	}

	payload := data
	uncompressedLength := uint32(0)
	if len(data) > 0 {
		payload = r.zstd.EncodeAll(data, nil)
		uncompressedLength = uint32(len(data))
	}
	sealed, err := r.master.Seal(nil, payload)
	if err != nil {
		return restic.ID{}, err
	}

	var packer *pack.Builder
	var entryType restic.BlobType
	switch blobType {
	case restic.DataBlob:
		packer = r.dataPacker
		if uncompressedLength > 0 {
			entryType = restic.CompressedDataBlob
		} else {
			entryType = restic.DataBlob
		}
	case restic.TreeBlob:
		packer = r.treePacker
		if uncompressedLength > 0 {
			entryType = restic.CompressedTreeBlob
		} else {
			entryType = restic.TreeBlob
		}
	}
	packer.Add(entryType, id, sealed, uncompressedLength)
	entry := index.Entry{
		Type:               blobType,
		ID:                 id,
		PackID:             restic.ID{}, // filled in when the pack is flushed
		Offset:             uint32(packer.Size() - len(sealed)),
		Length:             uint32(len(sealed)),
		UncompressedLength: uncompressedLength,
	}
	r.index.Add(entry)
	if blobType == restic.DataBlob {
		r.dataBlobs = append(r.dataBlobs, entry)
	} else {
		r.treeBlobs = append(r.treeBlobs, entry)
	}

	if packer.Size() >= DefaultPackSize {
		if err := r.flushPacker(ctx, blobType); err != nil {
			return restic.ID{}, err
		}
	}
	return id, nil
}

// Flush finalizes and stores all pending packs, then the index file that
// references them.
func (r *Repository) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	for _, blobType := range []restic.BlobType{restic.DataBlob, restic.TreeBlob} {
		if err := r.flushPacker(ctx, blobType); err != nil {
			return err
		}
	}
	return nil
}

// flushPacker writes the current pack of one type and its index entries.
// Caller holds saveMu.
func (r *Repository) flushPacker(ctx context.Context, blobType restic.BlobType) error {
	var packer *pack.Builder
	var blobs []index.Entry
	if blobType == restic.DataBlob {
		packer, blobs = r.dataPacker, r.dataBlobs
	} else {
		packer, blobs = r.treePacker, r.treeBlobs
	}
	if packer.Count() == 0 {
		return nil
	}

	packData, err := packer.Finalize(r.master)
	if err != nil {
		return fmt.Errorf("repository: finalize pack: %w", err)
	}
	packID := restic.Hash(packData)
	// 1. pack first
	if err := r.backend.Save(ctx, restic.Handle{Type: restic.DataFile, Name: packID.String()}, bytes.NewReader(packData)); err != nil {
		return fmt.Errorf("repository: save pack: %w", err)
	}

	// 2. entries now point at the stored pack
	docBlobs := make([]index.Blob, 0, len(blobs))
	for i := range blobs {
		blobs[i].PackID = packID
		r.index.Add(blobs[i])
		docBlobs = append(docBlobs, index.Blob{
			ID:                 blobs[i].ID,
			Type:               blobType,
			Offset:             blobs[i].Offset,
			Length:             blobs[i].Length,
			UncompressedLength: blobs[i].UncompressedLength,
		})
	}
	doc := index.Index{Packs: []index.Pack{{ID: packID, Blobs: docBlobs}}}

	// 3. index file last
	sealedIndex, err := index.Seal(doc, r.master)
	if err != nil {
		return fmt.Errorf("repository: seal index: %w", err)
	}
	indexName := restic.Hash(sealedIndex).String()
	if err := r.backend.Save(ctx, restic.Handle{Type: restic.IndexFile, Name: indexName}, bytes.NewReader(sealedIndex)); err != nil {
		return fmt.Errorf("repository: save index: %w", err)
	}

	// 4. reset packer state
	if blobType == restic.DataBlob {
		r.dataPacker = pack.NewBuilder()
		r.dataBlobs = nil
	} else {
		r.treePacker = pack.NewBuilder()
		r.treeBlobs = nil
	}
	return nil
}
