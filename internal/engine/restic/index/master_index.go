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
	Type               restic.BlobType
	ID                 restic.ID
	PackID             restic.ID
	Offset             uint32
	Length             uint32
	UncompressedLength uint32
}

// BlobHandle is the full identity of a blob in a restic repository. Data
// and tree blobs live in separate namespaces even when their plaintext hash
// is identical.
type BlobHandle struct {
	Type restic.BlobType
	ID   restic.ID
}

// MasterIndex is the in-memory map of every known blob, guarded by an
// RWMutex for concurrent chunk workers. Identity is (blob type, blob ID),
// matching restic's separate data/tree namespaces. Duplicate handles across
// packs use last-write-wins (dedup only needs one working location).
type MasterIndex struct {
	mu    sync.RWMutex
	blobs map[BlobHandle]Entry
}

func NewMasterIndex() *MasterIndex {
	return &MasterIndex{blobs: make(map[BlobHandle]Entry)}
}

// Lookup returns the storage location of a blob.
func (m *MasterIndex) Lookup(blobType restic.BlobType, id restic.ID) (Entry, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.blobs[BlobHandle{Type: blobType, ID: id}]
	return entry, ok
}

// Add inserts or overwrites one entry (last write wins).
func (m *MasterIndex) Add(entry Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[BlobHandle{Type: entry.Type, ID: entry.ID}] = entry
}

// AddIndex inserts every blob of an index file.
func (m *MasterIndex) AddIndex(idx Index) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, pack := range idx.Packs {
		for _, blob := range pack.Blobs {
			entry := Entry{
				Type:               blob.Type,
				ID:                 blob.ID,
				PackID:             pack.ID,
				Offset:             blob.Offset,
				Length:             blob.Length,
				UncompressedLength: blob.UncompressedLength,
			}
			m.blobs[BlobHandle{Type: entry.Type, ID: entry.ID}] = entry
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
