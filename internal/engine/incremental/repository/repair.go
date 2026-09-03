package repository

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/crypto"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/index"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/pack"
)

// RepairResult carries the count of objects processed during index repair.
type RepairResult struct {
	PacksProcessed    int
	BlobsIndexed      int
	OldIndexesRemoved int
	NewIndexesWritten int
}

// OpenForRepair opens a repository by validating the key and config, but
// without loading existing index files.
func OpenForRepair(ctx context.Context, b backend.Backend, password string) (*Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := b.Stat(ctx, incremental.Handle{Type: incremental.ConfigFile}); b.IsNotExist(err) {
		return nil, fmt.Errorf("%w: no config file found", incremental.ErrRepoNotFound)
	}
	master, err := unlockKeyFile(ctx, b, password)
	if err != nil {
		return nil, err
	}
	config, err := loadConfig(ctx, b, master)
	if err != nil {
		return nil, err
	}
	return newRepository(b, master, config)
}

// RepairIndex scans all packs in data/, rebuilds index documents from valid
// pack headers, writes new index files, and removes old index files.
// If any pack is corrupt or unreadable, RepairIndex aborts immediately
// without modifying repository state.
func (r *Repository) RepairIndex(ctx context.Context) (RepairResult, error) {
	if err := ctx.Err(); err != nil {
		return RepairResult{}, err
	}

	var oldIndexes []incremental.Handle
	if err := r.backend.List(ctx, incremental.IndexFile, func(h incremental.Handle, _ int64) error {
		oldIndexes = append(oldIndexes, h)
		return nil
	}); err != nil {
		return RepairResult{}, fmt.Errorf("repository: list index files: %w", err)
	}

	var packHandles []incremental.Handle
	if err := r.backend.List(ctx, incremental.DataFile, func(h incremental.Handle, _ int64) error {
		packHandles = append(packHandles, h)
		return nil
	}); err != nil {
		return RepairResult{}, fmt.Errorf("repository: list pack files: %w", err)
	}

	allPacks := make([]index.Pack, 0, len(packHandles))
	totalBlobs := 0

	for _, h := range packHandles {
		if err := ctx.Err(); err != nil {
			return RepairResult{}, err
		}

		packID, err := incremental.ParseID(h.Name)
		if err != nil {
			continue // not a pack object
		}

		info, err := r.backend.Stat(ctx, h)
		if err != nil {
			return RepairResult{}, fmt.Errorf("repository: stat pack %s: %w", packID, err)
		}
		if info.Size < 4 {
			return RepairResult{}, fmt.Errorf("repository: pack %s is too short to contain a header", packID)
		}

		var trailer []byte
		err = r.backend.Load(ctx, h, 4, info.Size-4, func(rd io.Reader) error {
			var readErr error
			trailer, readErr = io.ReadAll(rd)
			return readErr
		})
		if err != nil {
			return RepairResult{}, fmt.Errorf("repository: read trailer of pack %s: %w", packID, err)
		}

		headerLength := int(binary.LittleEndian.Uint32(trailer))
		if headerLength < crypto.Extension || int64(headerLength)+4 > info.Size {
			return RepairResult{}, fmt.Errorf("repository: invalid header length %d in pack %s", headerLength, packID)
		}

		var headerCiphertext []byte
		err = r.backend.Load(ctx, h, headerLength, info.Size-4-int64(headerLength), func(rd io.Reader) error {
			var readErr error
			headerCiphertext, readErr = io.ReadAll(rd)
			return readErr
		})
		if err != nil {
			return RepairResult{}, fmt.Errorf("repository: read header of pack %s: %w", packID, err)
		}

		plain, err := r.master.Open(nil, headerCiphertext)
		if err != nil {
			return RepairResult{}, fmt.Errorf("repository: decrypt header of pack %s: %w", packID, err)
		}

		entries, err := pack.ParseHeader(plain)
		if err != nil {
			return RepairResult{}, fmt.Errorf("repository: parse header of pack %s: %w", packID, err)
		}

		docBlobs := make([]index.Blob, 0, len(entries))
		for _, entry := range entries {
			var blobType incremental.BlobType
			switch entry.Type {
			case incremental.DataBlob, incremental.CompressedDataBlob:
			blobType = incremental.DataBlob
			case incremental.TreeBlob, incremental.CompressedTreeBlob:
			blobType = incremental.TreeBlob
			default:
				return RepairResult{}, fmt.Errorf("repository: unknown blob type %d in pack %s", entry.Type, packID)
			}
			docBlobs = append(docBlobs, index.Blob{
				ID:                 entry.ID,
				Type:               blobType,
				Offset:             entry.Offset,
				Length:             entry.Length,
				UncompressedLength: entry.UncompressedLength,
			})
			totalBlobs++
		}
		allPacks = append(allPacks, index.Pack{ID: packID, Blobs: docBlobs})
	}

	sort.Slice(allPacks, func(i, j int) bool {
		return bytes.Compare(allPacks[i].ID[:], allPacks[j].ID[:]) < 0
	})

	// Step 1: Write the new repaired index
	if err := r.writeIndex(ctx, allPacks); err != nil {
		return RepairResult{}, fmt.Errorf("repository: write repaired index: %w", err)
	}

	// Step 2: Remove old index files
	for _, h := range oldIndexes {
		if err := r.backend.Remove(ctx, h); err != nil {
			return RepairResult{}, fmt.Errorf("repository: remove old index %s: %w", h.Name, err)
		}
	}

	return RepairResult{
		PacksProcessed:    len(allPacks),
		BlobsIndexed:      totalBlobs,
		OldIndexesRemoved: len(oldIndexes),
		NewIndexesWritten: 1,
	}, nil
}
