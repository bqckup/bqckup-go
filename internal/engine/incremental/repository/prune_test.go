package repository

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/index"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/snapshot"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/tree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const prunePassword = "prune-test-password"

func pruneRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := Init(context.Background(), backend.NewLocal(t.TempDir()), prunePassword)
	require.NoError(t, err)
	return r
}

func writeSource(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// backupTestFiles stores one snapshot containing the given files (sorted
// tree, like the archiver) and flushes the packs.
func backupTestFiles(t *testing.T, r *Repository, files map[string]string, tags []string) {
	t.Helper()
	ctx := context.Background()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	nodes := make([]*tree.Node, 0, len(names))
	for _, name := range names {
		content := files[name]
		id, err := r.SaveBlob(ctx, incremental.DataBlob, []byte(content))
		require.NoError(t, err)
		nodes = append(nodes, &tree.Node{
			Name: name, Type: tree.TypeFile, Mode: 0o600, ModTime: time.Now(),
			UID: 1000, GID: 1000, Size: uint64(len(content)), Content: []incremental.ID{id},
		})
	}
	doc, err := (&tree.Tree{Nodes: nodes}).Marshal()
	require.NoError(t, err)
	treeID, err := r.SaveBlob(ctx, incremental.TreeBlob, doc)
	require.NoError(t, err)
	require.NoError(t, r.Flush(ctx))
	_, err = r.SaveSnapshot(ctx, snapshot.Snapshot{
		Time: time.Now(), Tree: &treeID, Paths: []string{"/test"}, Tags: tags,
	})
	require.NoError(t, err)
}

func packHandles(t *testing.T, b backend.Backend) []incremental.Handle {
	t.Helper()
	var packs []incremental.Handle
	err := b.List(context.Background(), incremental.DataFile, func(h incremental.Handle, _ int64) error {
		packs = append(packs, h)
		return nil
	})
	require.NoError(t, err)
	return packs
}

func packBytes(t *testing.T, b backend.Backend) int64 {
	t.Helper()
	total := int64(0)
	err := b.List(context.Background(), incremental.DataFile, func(h incremental.Handle, size int64) error {
		total += size
		return nil
	})
	require.NoError(t, err)
	return total
}

func indexHandles(t *testing.T, b backend.Backend) []incremental.Handle {
	t.Helper()
	var indexes []incremental.Handle
	err := b.List(context.Background(), incremental.IndexFile, func(h incremental.Handle, _ int64) error {
		indexes = append(indexes, h)
		return nil
	})
	require.NoError(t, err)
	return indexes
}

// TestForgetAndPruneReclaimsUnreferencedPacks: two snapshots, keep the
// newest, and verify the oldest pack bytes are
// reclaimed and only a consistent index remains.
func TestForgetAndPruneReclaimsUnreferencedPacks(t *testing.T) {
	ctx := context.Background()
	r := pruneRepo(t)
	b := r.backend

	backupTestFiles(t, r, map[string]string{
		"a.txt": strings.Repeat("a", 4096),
		"b.txt": strings.Repeat("b", 4096),
	}, []string{"bqckup", "site:test"})
	// second backup: change a, drop b, add c — a's and b's blobs become
	// unreferenced
	backupTestFiles(t, r, map[string]string{
		"a.txt": strings.Repeat("A", 4096),
		"c.txt": strings.Repeat("c", 4096),
	}, []string{"bqckup", "site:test"})

	snapshots, err := r.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, snapshots, 2)
	before := packBytes(t, b)

	result, err := r.ForgetAndPrune(ctx, 1, "site:test")
	require.NoError(t, err)
	assert.Equal(t, 1, result.SnapshotsRemoved)
	assert.Greater(t, result.BytesReclaimed, int64(0))
	assert.Greater(t, result.PacksRemoved, 0)

	snapshots, err = r.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Less(t, packBytes(t, b), before, "pack bytes must shrink after prune")

	// exactly one index file remains and it references only existing packs
	indexes := indexHandles(t, b)
	require.Len(t, indexes, 1)
	require.NoError(t, verifyIndexesReferenceExistingPacks(ctx, r, b))
}

// TestPartiallyReferencedPackSurvives builds two snapshots whose blobs
// share one pack: prune must keep the pack because one blob is still
// reachable (no repack in L2).
func TestPartiallyReferencedPackSurvives(t *testing.T) {
	ctx := context.Background()
	r := pruneRepo(t)
	b := r.backend

	blobX, err := r.SaveBlob(ctx, incremental.DataBlob, []byte("content-x"))
	require.NoError(t, err)
	blobY, err := r.SaveBlob(ctx, incremental.DataBlob, []byte("content-y"))
	require.NoError(t, err)
	require.NoError(t, r.Flush(ctx)) // both land in the same data pack

	// snapshot 1 references only X; snapshot 2 only Y (shared pack)
	tree1 := saveTestTree(t, r, blobX)
	tree2 := saveTestTree(t, r, blobY)
	_, err = r.SaveSnapshot(ctx, snapshot.Snapshot{
		Time: time.Now().Add(-time.Hour), Tree: &tree1, Tags: []string{"site:test"}, Paths: []string{"/x"},
	})
	require.NoError(t, err)
	_, err = r.SaveSnapshot(ctx, snapshot.Snapshot{
		Time: time.Now(), Tree: &tree2, Tags: []string{"site:test"}, Paths: []string{"/y"},
	})
	require.NoError(t, err)

	packsBefore := packHandles(t, b)
	require.Len(t, packsBefore, 3, "one data pack (X+Y) + one tree pack per snapshot")

	result, err := r.ForgetAndPrune(ctx, 1, "site:test")
	require.NoError(t, err)
	assert.Equal(t, 1, result.SnapshotsRemoved)

	// the shared data pack survives (it holds Y); only the dead tree pack
	// (t1) is removed
	packsAfter := packHandles(t, b)
	require.Len(t, packsAfter, 2, "data pack + surviving tree pack")
	for _, h := range packsAfter {
		assert.Contains(t, packsBefore, h, "no new packs during prune")
	}

	// X is still indexed (the pack keeps it), Y and tree2 are reachable
	_, ok := r.MasterIndex().Lookup(incremental.DataBlob, blobX)
	assert.True(t, ok)
	_, ok = r.MasterIndex().Lookup(incremental.DataBlob, blobY)
	assert.True(t, ok)
	require.NoError(t, verifyIndexesReferenceExistingPacks(ctx, r, b))
}

// TestPruneRemovesOrphanedPacks: a pack that exists but appears in no
// index (a crash between pack and index write) is garbage and must go.
func TestPruneRemovesOrphanedPacks(t *testing.T) {
	ctx := context.Background()
	r := pruneRepo(t)
	b := r.backend

	backupTestFiles(t, r, map[string]string{"f.txt": "data"}, []string{"bqckup", "site:test"})
	// plant an orphaned pack
	require.NoError(t, b.Save(ctx, incremental.Handle{Type: incremental.DataFile, Name: strings.Repeat("9", 64)}, strings.NewReader("orphan bytes")))

	_, err := r.ForgetAndPrune(ctx, 5, "site:test")
	require.NoError(t, err)
	for _, h := range packHandles(t, b) {
		assert.NotEqual(t, strings.Repeat("9", 64), h.Name, "orphaned pack must be deleted")
	}
}

// TestPruneWithoutForgetStillCleansGarbage: keepLast >= snapshot count
// deletes nothing but still prunes orphaned data.
func TestPruneWithoutForgetStillCleansGarbage(t *testing.T) {
	ctx := context.Background()
	r := pruneRepo(t)
	b := r.backend

	backupTestFiles(t, r, map[string]string{"f.txt": "data"}, []string{"bqckup", "site:test"})
	require.NoError(t, b.Save(ctx, incremental.Handle{Type: incremental.DataFile, Name: strings.Repeat("8", 64)}, strings.NewReader("garbage")))

	result, err := r.ForgetAndPrune(ctx, 5, "site:test")
	require.NoError(t, err)
	assert.Zero(t, result.SnapshotsRemoved)
	assert.Equal(t, 1, result.PacksRemoved, "orphan cleaned even without forget")
}

// TestForgetAndPruneKeepsSnapshotsWithOtherTags: two sites share one
// repository; forgetting+pruning site:a must never delete data that the
// site:b snapshot still references (restic prune semantics: used blobs are
// computed from all remaining snapshots, whatever their tags).
func TestForgetAndPruneKeepsSnapshotsWithOtherTags(t *testing.T) {
	ctx := context.Background()
	r := pruneRepo(t)
	b := r.backend

	// site:a has two snapshots (the older one is forgotten); site:b has one
	backupTestFiles(t, r, map[string]string{"a.txt": strings.Repeat("a", 4096)}, []string{"bqckup", "site:a"})
	backupTestFiles(t, r, map[string]string{"a2.txt": strings.Repeat("A", 4096)}, []string{"bqckup", "site:a"})
	packsAfterA2 := packHandles(t, b)
	backupTestFiles(t, r, map[string]string{"b.txt": strings.Repeat("b", 4096)}, []string{"bqckup", "site:b"})
	packsAfterB := packHandles(t, b)
	packsOfB := diffPacks(packsAfterB, packsAfterA2)
	require.NotEmpty(t, packsOfB, "site:b must own at least one pack")

	result, err := r.ForgetAndPrune(ctx, 1, "site:a")
	require.NoError(t, err)
	assert.Equal(t, 1, result.SnapshotsRemoved, "the oldest site:a snapshot is forgotten")

	snapshots, err := r.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, snapshots, 2, "newest site:a and the site:b snapshot survive")
	for _, h := range packsOfB {
		if _, err := b.Stat(ctx, h); err != nil {
			t.Fatalf("pack %s of the other-tag snapshot was pruned: data loss", h.Name)
		}
	}
}

// diffPacks returns the handles in newer that are not in older.
func diffPacks(newer, older []incremental.Handle) []incremental.Handle {
	out := make([]incremental.Handle, 0)
	for _, h := range newer {
		found := false
		for _, o := range older {
			if o.Name == h.Name {
				found = true
				break
			}
		}
		if !found {
			out = append(out, h)
		}
	}
	return out
}

// TestForgetAndPruneWithUnknownTagDeletesNothing: a tag matching no
// snapshot (renamed site, typo) must not prune a single byte — this was
// the worst-case data-loss scenario: kept was empty, so every pack in the
// repository was deleted in one run.
func TestForgetAndPruneWithUnknownTagDeletesNothing(t *testing.T) {
	ctx := context.Background()
	r := pruneRepo(t)
	b := r.backend

	backupTestFiles(t, r, map[string]string{"f.txt": "data"}, []string{"bqckup", "site:old-name"})
	before := packHandles(t, b)

	result, err := r.ForgetAndPrune(ctx, 1, "site:renamed")
	require.NoError(t, err)
	assert.Zero(t, result.SnapshotsRemoved)
	assert.Zero(t, result.PacksRemoved)
	assert.Zero(t, result.BytesReclaimed)
	assert.ElementsMatch(t, before, packHandles(t, b), "no pack may be pruned when no snapshot matches the tag")

	snapshots, err := r.ListSnapshots(ctx)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
}

func TestForgetAndPruneHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := pruneRepo(t)
	_, err := r.ForgetAndPrune(ctx, 1, "site:test")
	require.ErrorIs(t, err, context.Canceled)
}

// saveTestTree stores a one-file tree and returns its blob ID.
func saveTestTree(t *testing.T, r *Repository, contentID incremental.ID) incremental.ID {
	t.Helper()
	doc, err := (&tree.Tree{Nodes: []*tree.Node{{
		Name: "f", Type: tree.TypeFile, Mode: 0o600, ModTime: time.Now(),
		UID: 1000, GID: 1000, Size: 1, Content: []incremental.ID{contentID},
	}}}).Marshal()
	require.NoError(t, err)
	id, err := r.SaveBlob(context.Background(), incremental.TreeBlob, doc)
	require.NoError(t, err)
	require.NoError(t, r.Flush(context.Background()))
	return id
}

// verifyIndexesReferenceExistingPacks loads every index file and checks
// that each referenced pack exists — the crash-safety invariant.
func verifyIndexesReferenceExistingPacks(ctx context.Context, r *Repository, b backend.Backend) error {
	return b.List(ctx, incremental.IndexFile, func(h incremental.Handle, _ int64) error {
		var raw []byte
		err := b.Load(ctx, h, 0, 0, func(rd io.Reader) error {
			var readErr error
			raw, readErr = io.ReadAll(rd)
			return readErr
		})
		if err != nil {
			return err
		}
		idx, err := index.Open(raw, r.master)
		if err != nil {
			return err
		}
		for _, pack := range idx.Packs {
			if _, err := b.Stat(ctx, incremental.Handle{Type: incremental.DataFile, Name: pack.ID.String()}); err != nil {
				return err
			}
		}
		return nil
	})
}
