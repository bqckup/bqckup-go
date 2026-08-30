package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/snapshot"
	"github.com/klauspost/compress/zstd"
)

// SnapshotWithID pairs a stored snapshot document with its storage ID.
type SnapshotWithID struct {
	ID       incremental.ID
	Snapshot snapshot.Snapshot
}

// SaveSnapshot seals and stores a snapshot document. The storage ID is the
// SHA-256 of the encrypted bytes (same rule as keys and indexes).
func (r *Repository) SaveSnapshot(ctx context.Context, snap snapshot.Snapshot) (incremental.ID, error) {
	if err := ctx.Err(); err != nil {
		return incremental.ID{}, err
	}
	doc, err := json.Marshal(snap)
	if err != nil {
		return incremental.ID{}, fmt.Errorf("repository: marshal snapshot: %w", err)
	}
	sealed, err := r.master.Seal(nil, doc)
	if err != nil {
		return incremental.ID{}, err
	}
	id := incremental.Hash(sealed)
	h := incremental.Handle{Type: incremental.SnapshotFile, Name: id.String()}
	if err := r.backend.Save(ctx, h, bytes.NewReader(sealed)); err != nil {
		return incremental.ID{}, fmt.Errorf("repository: save snapshot: %w", err)
	}
	return id, nil
}

// ListSnapshots decrypts and parses every snapshot in the repository.
func (r *Repository) ListSnapshots(ctx context.Context) ([]SnapshotWithID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []SnapshotWithID
	err := r.backend.List(ctx, incremental.SnapshotFile, func(h incremental.Handle, size int64) error {
		entry, err := r.loadSnapshot(ctx, h)
		if err != nil {
			return err
		}
		out = append(out, entry)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repository: list snapshots: %w", err)
	}
	return out, nil
}

func (r *Repository) loadSnapshot(ctx context.Context, h incremental.Handle) (SnapshotWithID, error) {
	var raw []byte
	err := r.backend.Load(ctx, h, 0, 0, func(rd io.Reader) error {
		var err error
		raw, err = io.ReadAll(rd)
		return err
	})
	if err != nil {
		return SnapshotWithID{}, fmt.Errorf("repository: load snapshot: %w", err)
	}
	plain, err := r.master.Open(nil, raw)
	if err != nil {
		return SnapshotWithID{}, fmt.Errorf("repository: decrypt snapshot %s: %w", h.Name, err)
	}
	// The engine stores snapshots as plain JSON, but restic >= 0.17 writes
	// them through SaveUnpacked (0x02 || zstd(JSON), or 0x01 || JSON with
	// compression off — verification notes §2.11). Tolerate both.
	payload := plain
	if len(payload) > 0 {
		switch payload[0] {
		case 0x01:
			payload = payload[1:]
		case 0x02:
			decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
			if err != nil {
				return SnapshotWithID{}, fmt.Errorf("repository: parse snapshot %s: %w", h.Name, err)
			}
			defer decoder.Close()
			payload, err = decoder.DecodeAll(payload[1:], nil)
			if err != nil {
				return SnapshotWithID{}, fmt.Errorf("repository: parse snapshot %s: %w", h.Name, err)
			}
		}
	}
	var snap snapshot.Snapshot
	if err := json.Unmarshal(payload, &snap); err != nil {
		return SnapshotWithID{}, fmt.Errorf("repository: parse snapshot %s: %w", h.Name, err)
	}
	id, err := incremental.ParseID(h.Name)
	if err != nil {
		return SnapshotWithID{}, fmt.Errorf("repository: snapshot %s has an invalid name", h.Name)
	}
	return SnapshotWithID{ID: id, Snapshot: snap}, nil
}

// DeleteSnapshot removes one snapshot document (retention uses this; data
// blobs stay until L2 prune).
func (r *Repository) DeleteSnapshot(ctx context.Context, id incremental.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.backend.Remove(ctx, incremental.Handle{Type: incremental.SnapshotFile, Name: id.String()})
}
