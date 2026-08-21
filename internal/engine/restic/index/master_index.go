package index

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/crypto"
)

// Entry locates one blob: in which pack, at which offset.
type Entry struct {
	ID                 restic.ID
	PackID             restic.ID
	Offset             uint32
	Length             uint32
	UncompressedLength uint32
}

// MasterIndex is the in-memory map of every known blob, guarded by an
// RWMutex for concurrent chunk workers. Duplicate blob IDs across packs
// follow restic semantics: the last write wins (dedup only needs one
// working location; duplicates are cleaned up by prune in L2).
type MasterIndex struct {
	mu    sync.RWMutex
	blobs map[restic.ID]Entry
}

func NewMasterIndex() *MasterIndex {
	return &MasterIndex{blobs: make(map[restic.ID]Entry)}
}

// Lookup returns the storage location of a blob.
func (m *MasterIndex) Lookup(id restic.ID) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.blobs[id]
	return entry, ok
}

// Add inserts or overwrites one entry (last write wins).
func (m *MasterIndex) Add(entry Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[entry.ID] = entry
}

// AddIndex inserts every blob of an index file.
func (m *MasterIndex) AddIndex(idx Index) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pack := range idx.Packs {
		for _, blob := range pack.Blobs {
			m.blobs[blob.ID] = Entry{
				ID:                 blob.ID,
				PackID:             pack.ID,
				Offset:             blob.Offset,
				Length:             blob.Length,
				UncompressedLength: blob.UncompressedLength,
			}
		}
	}
}

// Len returns the number of indexed blobs.
func (m *MasterIndex) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.blobs)
}

// LoadAll loads every index file from the backend into the master index.
// This is what lets the engine continue repositories created by the real
// restic binary (product decision #11): existing index files are decrypted
// and parsed at open time.
func (m *MasterIndex) LoadAll(ctx context.Context, b backend.Backend, master *crypto.MasterKey) error {
	var handles []restic.Handle
	if err := b.List(ctx, restic.IndexFile, func(h restic.Handle, size int64) error {
		handles = append(handles, h)
		return nil
	}); err != nil {
		return fmt.Errorf("index: list index files: %w", err)
	}
	for _, h := range handles {
		err := b.Load(ctx, h, 0, 0, func(rd io.Reader) error {
			data, err := io.ReadAll(rd)
			if err != nil {
				return err
			}
			idx, err := Open(data, master)
			if err != nil {
				return fmt.Errorf("index: load %s: %w", h.Name, err)
			}
			m.AddIndex(idx)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
