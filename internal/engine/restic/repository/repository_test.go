package repository

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/crypto"
	"github.com/bqckup/bqckup-go/internal/engine/restic/snapshot"
	"github.com/bqckup/bqckup-go/internal/engine/restic/tree"
	"github.com/klauspost/compress/zstd"
)

const testPassword = "test-repository-password"

func newRepo(t *testing.T, ctx context.Context) (*Repository, *backend.Local) {
	t.Helper()
	local := backend.NewLocal(t.TempDir())
	repo, err := Init(ctx, local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return repo, local
}

func TestInitCreatesLayout(t *testing.T) {
	ctx := context.Background()
	local := backend.NewLocal(t.TempDir())
	repo, err := Init(ctx, local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if repo.config.Version != 2 || repo.config.ID.IsNull() || repo.config.ChunkerPolynomial.Deg() != 53 {
		t.Fatalf("unexpected config: %+v", repo.config)
	}
	// config file exists at the root
	info, err := local.Stat(ctx, restic.Handle{Type: restic.ConfigFile})
	if err != nil {
		t.Fatal(err)
	}
	if info.Size <= crypto.Extension {
		t.Fatalf("config file too small: %d", info.Size)
	}
	// exactly one key file, named by the SHA-256 of its bytes
	var keyNames []string
	if err := local.List(ctx, restic.KeyFileType, func(h restic.Handle, size int64) error {
		keyNames = append(keyNames, h.Name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(keyNames) != 1 {
		t.Fatalf("want 1 key file, got %d", len(keyNames))
	}
	var keyBytes []byte
	if err := local.Load(ctx, restic.Handle{Type: restic.KeyFileType, Name: keyNames[0]}, 0, 0, func(rd io.Reader) error {
		keyBytes, err = io.ReadAll(rd)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if restic.Hash(keyBytes).String() != keyNames[0] {
		t.Fatal("key file name is not the SHA-256 of its bytes")
	}
	// all 256 data dirs + other dirs exist with 0700
	for i := 0; i < 256; i++ {
		dir := filepath.Join(local.DirPath(), "data", fmt.Sprintf("%02x", i))
		if err := checkDirMode(dir); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"keys", "index", "snapshots", "locks", "tmp"} {
		if err := checkDirMode(filepath.Join(local.DirPath(), dir)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInitIdempotent(t *testing.T) {
	ctx := context.Background()
	local := backend.NewLocal(t.TempDir())
	first, err := Init(ctx, local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	originalID := first.config.ID
	// save a blob so we can prove data survives re-init
	if _, err := first.SaveBlob(ctx, restic.DataBlob, []byte("keep me")); err != nil {
		t.Fatal(err)
	}
	if err := first.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	second, err := Init(ctx, local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if second.config.ID != originalID {
		t.Fatal("re-init created a new repository id")
	}
	var keyCount int
	if err := local.List(ctx, restic.KeyFileType, func(restic.Handle, int64) error {
		keyCount++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if keyCount != 1 {
		t.Fatalf("re-init created %d key files, want 1", keyCount)
	}
	if _, ok := second.index.Lookup(restic.DataBlob, restic.Hash([]byte("keep me"))); !ok {
		t.Fatal("data blob lost after re-init")
	}
}

type failFirstKeySaveBackend struct {
	backend.Backend
	failed bool
}

func (b *failFirstKeySaveBackend) Save(ctx context.Context, h restic.Handle, rd io.Reader) error {
	if h.Type == restic.KeyFileType && !b.failed {
		b.failed = true
		return errors.New("injected key save failure")
	}
	return b.Backend.Save(ctx, h, rd)
}

func TestInitRecoversAfterKeySaveFailure(t *testing.T) {
	ctx := context.Background()
	b := &failFirstKeySaveBackend{Backend: backend.NewLocal(t.TempDir())}
	if _, err := Init(ctx, b, testPassword); err == nil {
		t.Fatal("first init should fail while saving the key")
	}
	if _, err := Init(ctx, b, testPassword); err != nil {
		t.Fatalf("retry must repair or restart partial init: %v", err)
	}
}

func TestOpenWrongPassword(t *testing.T) {
	ctx := context.Background()
	_, local := newRepo(t, ctx)
	_, err := Open(ctx, local, "wrong-password")
	if !errors.Is(err, restic.ErrInvalidPassword) {
		t.Fatalf("want ErrInvalidPassword, got %v", err)
	}
	if bytes.Contains([]byte(err.Error()), []byte("wrong-password")) {
		t.Fatal("error leaks the password")
	}
	// the right password works
	if _, err := Open(ctx, local, testPassword); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMissingRepo(t *testing.T) {
	_, err := Open(context.Background(), backend.NewLocal(t.TempDir()), testPassword)
	if !errors.Is(err, restic.ErrRepoNotFound) {
		t.Fatalf("want ErrRepoNotFound, got %v", err)
	}
}

func TestSaveBlobDedup(t *testing.T) {
	ctx := context.Background()
	repo, local := newRepo(t, ctx)
	data := bytes.Repeat([]byte{0x42}, 1000)
	firstID, err := repo.SaveBlob(ctx, restic.DataBlob, data)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := repo.SaveBlob(ctx, restic.DataBlob, data)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != secondID {
		t.Fatal("same data produced different IDs")
	}
	if err := repo.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// exactly one data blob in one pack on disk
	var packCount int
	if err := local.List(ctx, restic.DataFile, func(restic.Handle, int64) error {
		packCount++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if packCount != 1 {
		t.Fatalf("want 1 pack file, got %d", packCount)
	}
}

func TestSaveBlobKeepsDataAndTreeTypesSeparate(t *testing.T) {
	ctx := context.Background()
	repo, local := newRepo(t, ctx)
	payload := []byte(`{"nodes":[]}`)
	dataID, err := repo.SaveBlob(ctx, restic.DataBlob, payload)
	if err != nil {
		t.Fatal(err)
	}
	treeID, err := repo.SaveBlob(ctx, restic.TreeBlob, payload)
	if err != nil {
		t.Fatal(err)
	}
	if dataID != treeID {
		t.Fatal("fixture must produce equal content IDs")
	}
	if err := repo.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.MasterIndex().Len() != 2 {
		t.Fatalf("data and tree handles must both be indexed, got %d entry", reopened.MasterIndex().Len())
	}
	dataEntry, dataOK := reopened.MasterIndex().Lookup(restic.DataBlob, dataID)
	treeEntry, treeOK := reopened.MasterIndex().Lookup(restic.TreeBlob, treeID)
	if !dataOK || !treeOK {
		t.Fatalf("typed lookups missing: data=%v tree=%v", dataOK, treeOK)
	}
	if dataEntry.PackID == treeEntry.PackID {
		t.Fatal("data and tree blobs unexpectedly resolved to the same pack")
	}
}

func TestFlushThenReopenFindsBlobs(t *testing.T) {
	ctx := context.Background()
	repo, local := newRepo(t, ctx)
	blobs := map[string]restic.BlobType{
		"alpha": restic.DataBlob,
		"beta":  restic.TreeBlob,
		"":      restic.DataBlob, // zero-length: never compressed
	}
	ids := map[string]restic.ID{}
	for name, blobType := range blobs {
		id, err := repo.SaveBlob(ctx, blobType, []byte(name))
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	if err := repo.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	// packs exist before any index references them: every index entry must
	// resolve to a stored pack file
	var packNames []string
	if err := local.List(ctx, restic.DataFile, func(h restic.Handle, size int64) error {
		packNames = append(packNames, h.Name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(packNames) < 2 {
		t.Fatalf("want separate data and tree packs, got %d pack(s)", len(packNames))
	}
	var indexCount int
	if err := local.List(ctx, restic.IndexFile, func(restic.Handle, int64) error {
		indexCount++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if indexCount == 0 {
		t.Fatal("no index file written")
	}

	reopened, err := Open(ctx, local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for name, id := range ids {
		entry, ok := reopened.index.Lookup(blobs[name], id)
		if !ok {
			t.Fatalf("blob %q missing after reopen", name)
		}
		if entry.PackID.IsNull() || entry.Length == 0 {
			t.Fatalf("blob %q has bogus entry %+v", name, entry)
		}
	}
	// dedup across runs: saving the same data again stores nothing new
	if _, err := reopened.SaveBlob(ctx, restic.DataBlob, []byte("alpha")); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var finalPackCount int
	if err := local.List(ctx, restic.DataFile, func(restic.Handle, int64) error {
		finalPackCount++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if finalPackCount != len(packNames) {
		t.Fatalf("dedup across runs wrote new packs: %d -> %d", len(packNames), finalPackCount)
	}
}

func TestZeroLengthBlobStoredUncompressed(t *testing.T) {
	ctx := context.Background()
	repo, local := newRepo(t, ctx)
	id, err := repo.SaveBlob(ctx, restic.DataBlob, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	entry, ok := repo.index.Lookup(restic.DataBlob, id)
	if !ok {
		t.Fatal("empty blob missing from index")
	}
	if entry.UncompressedLength != 0 {
		t.Fatalf("empty blob marked compressed: %+v", entry)
	}
	if err := local.List(ctx, restic.IndexFile, func(restic.Handle, int64) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestLargeBlobFlushesPack(t *testing.T) {
	ctx := context.Background()
	repo, local := newRepo(t, ctx)
	// incompressible data so zstd cannot shrink it below the threshold
	big := make([]byte, DefaultPackSize+1024)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveBlob(ctx, restic.DataBlob, big); err != nil {
		t.Fatal(err)
	}
	// the pack must have been flushed automatically once over the threshold
	var packCount int
	if err := local.List(ctx, restic.DataFile, func(restic.Handle, int64) error {
		packCount++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if packCount == 0 {
		t.Fatal("pack over threshold was not flushed")
	}
}

func TestLoadTreeRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo, _ := newRepo(t, ctx)
	doc, err := (&tree.Tree{Nodes: []*tree.Node{
		{Name: "alpha", Type: tree.TypeFile, Mode: 0o644},
		{Name: "beta", Type: tree.TypeDir, Mode: 0o755},
	}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	id, err := repo.SaveBlob(ctx, restic.TreeBlob, doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	loaded, err := repo.LoadTree(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Nodes) != 2 || loaded.Nodes[0].Name != "alpha" || loaded.Nodes[1].Name != "beta" {
		t.Fatalf("unexpected tree: %+v", loaded)
	}
}

func TestLoadSnapshotByID(t *testing.T) {
	ctx := context.Background()
	repo, _ := newRepo(t, ctx)
	snap := snapshot.Snapshot{
		Time:     time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC),
		Hostname: "host-a",
		Tags:     []string{"site:site-b"},
	}
	id, err := repo.SaveSnapshot(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.LoadSnapshot(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != id || loaded.Snapshot.Hostname != "host-a" || len(loaded.Snapshot.Tags) != 1 {
		t.Fatalf("unexpected snapshot: %+v", loaded)
	}
}

// TestLoadSnapshotToleratesResticUnpackedEnvelope covers snapshots written
// by restic >= 0.17, whose SaveUnpacked path stores 0x02 || zstd(JSON)
// (0x01 || JSON with compression off) instead of the engine's plain JSON
// (restic-format-verification.md §2.11). Listing must not fail on them or
// retention breaks for the whole repository.
func TestLoadSnapshotToleratesResticUnpackedEnvelope(t *testing.T) {
	ctx := context.Background()
	repo, _ := newRepo(t, ctx)
	snap := snapshot.Snapshot{
		Time:     time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC),
		Hostname: "host-a",
		Tags:     []string{"site:site-b"},
	}
	doc, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	for _, plain := range [][]byte{
		append([]byte{2}, encoder.EncodeAll(doc, nil)...), // compressed unpacked blob
		append([]byte{1}, doc...),                         // uncompressed unpacked blob
	} {
		sealed, err := repo.master.Seal(nil, plain)
		if err != nil {
			t.Fatal(err)
		}
		id := restic.Hash(sealed)
		if err := repo.backend.Save(ctx, restic.Handle{Type: restic.SnapshotFile, Name: id.String()}, bytes.NewReader(sealed)); err != nil {
			t.Fatal(err)
		}
		loaded, err := repo.LoadSnapshot(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.ID != id || loaded.Snapshot.Hostname != "host-a" {
			t.Fatalf("unexpected snapshot: %+v", loaded)
		}
	}
	listed, err := repo.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(listed))
	}
}

func TestLoadBlobMissingIDFails(t *testing.T) {
	ctx := context.Background()
	repo, _ := newRepo(t, ctx)
	if _, err := repo.LoadBlob(ctx, restic.DataBlob, restic.Hash([]byte("missing"))); err == nil {
		t.Fatal("missing blob ID must fail")
	}
}

func TestLoadTreeMalformedJSONFails(t *testing.T) {
	ctx := context.Background()
	repo, _ := newRepo(t, ctx)
	id, err := repo.SaveBlob(ctx, restic.TreeBlob, []byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadTree(ctx, id); err == nil {
		t.Fatal("malformed tree must fail to parse")
	}
}

func checkDirMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New(path + " mode is not 0700")
	}
	return nil
}
