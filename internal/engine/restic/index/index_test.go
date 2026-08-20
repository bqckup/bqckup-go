package index

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/crypto"
)

func testMaster(t *testing.T) *crypto.MasterKey {
	t.Helper()
	key, err := crypto.NewRandomMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func sampleIndex(t *testing.T) Index {
	t.Helper()
	packID := restic.Hash([]byte("pack-one"))
	return Index{Packs: []Pack{{
		ID: packID,
		Blobs: []Blob{
			{ID: restic.Hash([]byte("blob-a")), Type: restic.DataBlob, Offset: 0, Length: 524320},
			{ID: restic.Hash([]byte("blob-b")), Type: restic.CompressedDataBlob, Offset: 524320, Length: 90000, UncompressedLength: 1048576},
			{ID: restic.Hash([]byte("blob-c")), Type: restic.TreeBlob, Offset: 614320, Length: 431},
		},
	}}}
}

func TestSealOpenRoundTrip(t *testing.T) {
	master := testMaster(t)
	idx := sampleIndex(t)
	sealed, err := Seal(idx, master)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) <= crypto.Extension {
		t.Fatalf("sealed index too small: %d bytes", len(sealed))
	}
	restored, err := Open(sealed, master)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Packs) != 1 || len(restored.Packs[0].Blobs) != 3 {
		t.Fatalf("unexpected restore: %+v", restored)
	}
	original := idx.Packs[0].Blobs
	got := restored.Packs[0].Blobs
	for i := range original {
		// The JSON spelling only distinguishes data/tree: compressed blobs
		// normalize to DataBlob/TreeBlob with UncompressedLength set.
		if original[i].Type.String() != got[i].Type.String() {
			t.Fatalf("blob %d type mismatch: %v vs %v", i, original[i].Type, got[i].Type)
		}
		if original[i].ID != got[i].ID ||
			original[i].Offset != got[i].Offset ||
			original[i].Length != got[i].Length ||
			original[i].UncompressedLength != got[i].UncompressedLength {
			t.Fatalf("blob %d mismatch: %+v vs %+v", i, original[i], got[i])
		}
	}
	if restored.Packs[0].ID != idx.Packs[0].ID {
		t.Fatal("pack id mismatch")
	}
}

func TestJSONShapeMatchesRestic(t *testing.T) {
	// Hand-written fixture in the exact shape verified in
	// restic-format-verification.md §2.7.
	doc := []byte(`{"packs":[{"id":"73d04e6125cf3c28a299cc2f3cca3b78ceac396e4fcf9575e34536b26782413c","blobs":[{"id":"3ec79977ef0cf5de7b08cd12b874cd0f62bbaf7f07f3497a5b1bbcc8cb39b1ce","type":"data","offset":0,"length":524320,"uncompressed_length":1048576}]}]}`)
	var idx Index
	if err := json.Unmarshal(doc, &idx); err != nil {
		t.Fatal(err)
	}
	blob := idx.Packs[0].Blobs[0]
	if blob.Type != restic.DataBlob {
		t.Fatalf("type = %v, want data", blob.Type)
	}
	if blob.UncompressedLength != 1048576 || blob.Length != 524320 || blob.Offset != 0 {
		t.Fatalf("unexpected blob: %+v", blob)
	}
	// serializing back must keep restic's field names
	out, err := json.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"id"`, `"type":"data"`, `"offset"`, `"length"`, `"uncompressed_length"`} {
		if !bytes.Contains(out, []byte(field)) {
			t.Fatalf("serialized index misses %s: %s", field, out)
		}
	}
}

func TestWrongVersionByteRejected(t *testing.T) {
	master := testMaster(t)
	idx := sampleIndex(t)
	sealed, err := Seal(idx, master)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := master.Open(nil, sealed)
	if err != nil {
		t.Fatal(err)
	}
	plain[0] = 0x01 // corrupt the version byte, then re-seal so the MAC passes
	tampered, err := master.Seal(nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(tampered, master); err == nil {
		t.Fatal("want error for unsupported version byte")
	}
}

func TestMasterIndexLookup10k(t *testing.T) {
	masterIndex := NewMasterIndex()
	const count = 10000
	entries := make(map[restic.ID]Entry, count)
	for i := 0; i < count; i++ {
		id := restic.Hash([]byte{byte(i), byte(i >> 8)})
		entry := Entry{ID: id, PackID: restic.Hash([]byte("pack")), Offset: uint32(i), Length: 100}
		entries[id] = entry
		masterIndex.Add(entry)
	}
	if masterIndex.Len() != count {
		t.Fatalf("len = %d, want %d", masterIndex.Len(), count)
	}
	for id, want := range entries {
		got, ok := masterIndex.Lookup(id)
		if !ok || got != want {
			t.Fatalf("lookup %s = %+v, want %+v", id, got, want)
		}
	}
	if _, ok := masterIndex.Lookup(restic.Hash([]byte("missing"))); ok {
		t.Fatal("found a blob that was never added")
	}
}

func TestMasterIndexDuplicateLastWriteWins(t *testing.T) {
	masterIndex := NewMasterIndex()
	id := restic.Hash([]byte("dup"))
	masterIndex.Add(Entry{ID: id, PackID: restic.Hash([]byte("pack-a")), Offset: 1})
	masterIndex.Add(Entry{ID: id, PackID: restic.Hash([]byte("pack-b")), Offset: 2})
	got, ok := masterIndex.Lookup(id)
	if !ok {
		t.Fatal("entry missing")
	}
	if got.PackID != restic.Hash([]byte("pack-b")) || got.Offset != 2 {
		t.Fatalf("want last write to win, got %+v", got)
	}
}

func TestMasterIndexGoroutineStorm(t *testing.T) {
	masterIndex := NewMasterIndex()
	const writers = 8
	const readers = 8
	const perWriter = 500
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := restic.Hash([]byte{byte(w), byte(i), byte(i >> 8)})
				masterIndex.Add(Entry{ID: id, PackID: restic.Hash([]byte("pack")), Offset: uint32(i)})
			}
		}(w)
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < writers*perWriter; i++ {
				masterIndex.Lookup(restic.Hash([]byte{byte(i)}))
				masterIndex.Len()
			}
		}()
	}
	wg.Wait()
	if masterIndex.Len() != writers*perWriter {
		t.Fatalf("len = %d, want %d", masterIndex.Len(), writers*perWriter)
	}
}

func TestLoadAllFromBackend(t *testing.T) {
	master := testMaster(t)
	ctx := context.Background()
	repo := backend.NewLocal(t.TempDir())

	saveIndex := func(idx Index) string {
		t.Helper()
		sealed, err := Seal(idx, master)
		if err != nil {
			t.Fatal(err)
		}
		name := restic.Hash(sealed).String()
		h := restic.Handle{Type: restic.IndexFile, Name: name}
		if err := repo.Save(ctx, h, bytes.NewReader(sealed)); err != nil {
			t.Fatal(err)
		}
		return name
	}

	first := sampleIndex(t)
	second := Index{Packs: []Pack{{
		ID: restic.Hash([]byte("pack-two")),
		Blobs: []Blob{
			{ID: restic.Hash([]byte("blob-d")), Type: restic.TreeBlob, Offset: 0, Length: 220},
		},
	}}}
	saveIndex(first)
	saveIndex(second)

	masterIndex := NewMasterIndex()
	if err := masterIndex.LoadAll(ctx, repo, master); err != nil {
		t.Fatal(err)
	}
	if masterIndex.Len() != 4 {
		t.Fatalf("loaded %d blobs, want 4", masterIndex.Len())
	}
	for _, want := range []struct {
		id     string
		pack   string
		offset uint32
	}{
		{"blob-a", "pack-one", 0},
		{"blob-b", "pack-one", 524320},
		{"blob-c", "pack-one", 614320},
		{"blob-d", "pack-two", 0},
	} {
		entry, ok := masterIndex.Lookup(restic.Hash([]byte(want.id)))
		if !ok {
			t.Fatalf("blob %s not found after LoadAll", want.id)
		}
		if entry.PackID != restic.Hash([]byte(want.pack)) || entry.Offset != want.offset {
			t.Fatalf("blob %s entry = %+v", want.id, entry)
		}
	}
}

func TestLoadAllCorruptIndexFails(t *testing.T) {
	master := testMaster(t)
	ctx := context.Background()
	repo := backend.NewLocal(t.TempDir())
	sealed, err := Seal(sampleIndex(t), master)
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)/2] ^= 0xff
	name := restic.Hash(sealed).String()
	if err := repo.Save(ctx, restic.Handle{Type: restic.IndexFile, Name: name}, bytes.NewReader(sealed)); err != nil {
		t.Fatal(err)
	}
	if err := NewMasterIndex().LoadAll(ctx, repo, master); err == nil {
		t.Fatal("want error loading a corrupt index")
	}
}
