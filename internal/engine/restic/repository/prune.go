package repository

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/index"
	"github.com/bqckup/bqckup-go/internal/engine/restic/tree"
	"github.com/klauspost/compress/zstd"
)

// PruneResult reports what forget+prune removed.
type PruneResult struct {
	SnapshotsRemoved int
	PacksRemoved     int
	BytesReclaimed   int64
}

// ForgetAndPrune implements L2 retention: snapshot files beyond keep_last
// for the site tag are deleted, then space is reclaimed with a
// mark-and-sweep prune. Reachable blobs are computed from EVERY snapshot
// still in the repository (whatever its tags), exactly like restic's prune
// (getUsedBlobs walks all remaining snapshots): forgetting one site must
// never delete data another snapshot still references. The sweep order is
// the inverse of backup — the new index (without dead packs) is written
// and the old index files are removed BEFORE any pack is deleted — so a
// crash at any point leaves the repository consistent and `restic check`
// green.
//
// No repack: a pack holding any reachable blob survives whole, including
// its unreachable blobs (repack is a recorded future option). Packs that
// exist but appear in no index are orphaned (e.g. a crash between pack and
// index write) and are deleted like dead packs.
func (r *Repository) ForgetAndPrune(ctx context.Context, keepLast int, siteTag string) (PruneResult, error) {
	var result PruneResult
	if keepLast < 1 {
		return result, fmt.Errorf("keep_last must be at least 1")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	snapshots, err := r.ListSnapshots(ctx)
	if err != nil {
		return result, fmt.Errorf("repository: list snapshots for retention: %w", err)
	}
	mine := snapshotsForTag(snapshots, siteTag)
	sort.Slice(mine, func(i, j int) bool { return mine[i].Snapshot.Time.Before(mine[j].Snapshot.Time) })

	toForget := []SnapshotWithID(nil)
	if len(mine) > keepLast {
		toForget = mine[:len(mine)-keepLast]
	}
	for _, entry := range toForget {
		if err := r.DeleteSnapshot(ctx, entry.ID); err != nil {
			return result, fmt.Errorf("repository: delete snapshot %s: %w", entry.ID, err)
		}
		result.SnapshotsRemoved++
	}

	// Mark reachable blobs from every snapshot that REMAINS (any tag).
	// Marking only the kept site-tagged snapshots would silently prune the
	// data of snapshots with other or no tags, and — when no snapshot
	// matches the tag (renamed site, typo) — would delete every pack in the
	// repository in a single run.
	remaining, err := r.ListSnapshots(ctx)
	if err != nil {
		return result, fmt.Errorf("repository: list snapshots for prune: %w", err)
	}
	reachable := make(map[index.BlobHandle]struct{})
	for _, entry := range remaining {
		if entry.Snapshot.Tree == nil {
			continue
		}
		if err := r.markTree(ctx, *entry.Snapshot.Tree, reachable); err != nil {
			return result, fmt.Errorf("repository: mark snapshot %s: %w", entry.ID, err)
		}
	}

	if err := r.sweep(ctx, reachable, &result); err != nil {
		return result, err
	}
	return result, nil
}

// snapshotsForTag returns the snapshots carrying the exact tag.
func snapshotsForTag(snapshots []SnapshotWithID, siteTag string) []SnapshotWithID {
	out := make([]SnapshotWithID, 0, len(snapshots))
	for _, entry := range snapshots {
		for _, candidate := range entry.Snapshot.Tags {
			if candidate == siteTag {
				out = append(out, entry)
				break
			}
		}
	}
	return out
}

// markTree walks one tree and records every reachable blob ID (tree and
// data). Trees form a DAG, so the visited set doubles as dedup.
// ponytail: map-based reachable set; ~50 bytes per blob. If a repository
// grows beyond tens of millions of blobs, switch to a bitset keyed by
// index position (measured need first, per L2-D1).
func (r *Repository) markTree(ctx context.Context, treeID restic.ID, reachable map[index.BlobHandle]struct{}) error {
	treeHandle := index.BlobHandle{Type: restic.TreeBlob, ID: treeID}
	if _, seen := reachable[treeHandle]; seen {
		return nil
	}
	reachable[treeHandle] = struct{}{}
	plain, err := r.loadBlob(ctx, restic.TreeBlob, treeID)
	if err != nil {
		return err
	}
	t, err := tree.Unmarshal(plain)
	if err != nil {
		return fmt.Errorf("repository: parse tree %s: %w", treeID, err)
	}
	for _, node := range t.Nodes {
		if node.Subtree != nil {
			if err := r.markTree(ctx, *node.Subtree, reachable); err != nil {
				return err
			}
		}
		for _, contentID := range node.Content {
			reachable[index.BlobHandle{Type: restic.DataBlob, ID: contentID}] = struct{}{}
		}
	}
	return nil
}

// loadBlob reads and decrypts one blob by ID through the master index,
// decompressing it when the index says it was compressed.
func (r *Repository) loadBlob(ctx context.Context, blobType restic.BlobType, id restic.ID) ([]byte, error) {
	entry, ok := r.index.Lookup(blobType, id)
	if !ok {
		return nil, fmt.Errorf("repository: blob %s is not indexed", id)
	}
	var sealed []byte
	err := r.backend.Load(ctx, restic.Handle{Type: restic.DataFile, Name: entry.PackID.String()}, int(entry.Length), int64(entry.Offset), func(rd io.Reader) error {
		var err error
		sealed, err = io.ReadAll(rd)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("repository: load blob %s: %w", id, err)
	}
	plain, err := r.master.Open(nil, sealed)
	if err != nil {
		return nil, fmt.Errorf("repository: decrypt blob %s: %w", id, err)
	}
	if entry.UncompressedLength == 0 {
		return plain, nil
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	decoded, err := decoder.DecodeAll(plain, nil)
	if err != nil {
		return nil, fmt.Errorf("repository: decompress blob %s: %w", id, err)
	}
	return decoded, nil
}

// sweep writes the new index without dead packs, removes the old index
// files, then deletes dead and orphaned packs.
func (r *Repository) sweep(ctx context.Context, reachable map[index.BlobHandle]struct{}, result *PruneResult) error {
	var oldIndexes []restic.Handle
	packs := make(map[restic.ID]index.Pack)
	if err := r.backend.List(ctx, restic.IndexFile, func(h restic.Handle, _ int64) error {
		oldIndexes = append(oldIndexes, h)
		var raw []byte
		err := r.backend.Load(ctx, h, 0, 0, func(rd io.Reader) error {
			var err error
			raw, err = io.ReadAll(rd)
			return err
		})
		if err != nil {
			return err
		}
		idx, err := index.Open(raw, r.master)
		if err != nil {
			return fmt.Errorf("repository: load index %s: %w", h.Name, err)
		}
		for _, pack := range idx.Packs {
			packs[pack.ID] = pack
		}
		return nil
	}); err != nil {
		return fmt.Errorf("repository: list indexes for prune: %w", err)
	}

	// keep packs holding at least one reachable blob; everything else dies
	keptPacks := make([]index.Pack, 0, len(packs))
	deadPacks := make([]restic.ID, 0, len(packs))
	for packID, pack := range packs {
		if packHasReachableBlob(pack, reachable) {
			keptPacks = append(keptPacks, pack)
		} else {
			deadPacks = append(deadPacks, packID)
		}
	}
	sort.Slice(keptPacks, func(i, j int) bool { return bytes.Compare(keptPacks[i].ID[:], keptPacks[j].ID[:]) < 0 })

	// 1. write the new index (before any deletion)
	if err := r.writeIndex(ctx, keptPacks); err != nil {
		return err
	}
	// 2. remove the old index files
	for _, h := range oldIndexes {
		if err := r.backend.Remove(ctx, h); err != nil {
			return fmt.Errorf("repository: remove old index %s: %w", h.Name, err)
		}
	}

	// 3. delete dead packs, then orphaned packs (in data/ but not indexed)
	for _, packID := range deadPacks {
		if err := r.deletePack(ctx, packID, result); err != nil {
			return err
		}
	}
	indexed := make(map[restic.ID]struct{}, len(packs))
	for packID := range packs {
		indexed[packID] = struct{}{}
	}
	if err := r.backend.List(ctx, restic.DataFile, func(h restic.Handle, _ int64) error {
		packID, err := restic.ParseID(h.Name)
		if err != nil {
			return nil // not a pack name; leave it alone
		}
		if _, known := indexed[packID]; known {
			return nil
		}
		return r.deletePack(ctx, packID, result)
	}); err != nil {
		return fmt.Errorf("repository: list packs for prune: %w", err)
	}
	return nil
}

func packHasReachableBlob(pack index.Pack, reachable map[index.BlobHandle]struct{}) bool {
	for _, blob := range pack.Blobs {
		if _, ok := reachable[index.BlobHandle{Type: blob.Type, ID: blob.ID}]; ok {
			return true
		}
	}
	return false
}

// writeIndex seals and stores one index file with the kept packs.
func (r *Repository) writeIndex(ctx context.Context, keptPacks []index.Pack) error {
	doc := index.Index{Packs: keptPacks}
	sealed, err := index.Seal(doc, r.master)
	if err != nil {
		return fmt.Errorf("repository: seal prune index: %w", err)
	}
	name := restic.Hash(sealed).String()
	if err := r.backend.Save(ctx, restic.Handle{Type: restic.IndexFile, Name: name}, bytes.NewReader(sealed)); err != nil {
		return fmt.Errorf("repository: save prune index: %w", err)
	}
	return nil
}

// deletePack removes one pack and accounts for its size.
func (r *Repository) deletePack(ctx context.Context, packID restic.ID, result *PruneResult) error {
	h := restic.Handle{Type: restic.DataFile, Name: packID.String()}
	info, err := r.backend.Stat(ctx, h)
	if err == nil {
		result.BytesReclaimed += info.Size
	} else if !r.backend.IsNotExist(err) {
		return fmt.Errorf("repository: stat pack %s: %w", packID, err)
	}
	if err := r.backend.Remove(ctx, h); err != nil {
		return fmt.Errorf("repository: delete pack %s: %w", packID, err)
	}
	result.PacksRemoved++
	return nil
}
