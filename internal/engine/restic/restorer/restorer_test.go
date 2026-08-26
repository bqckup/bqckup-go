package restorer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/engine/restic"
	"github.com/bqckup/bqckup-go/internal/engine/restic/archiver"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
	"github.com/bqckup/bqckup-go/internal/engine/restic/snapshot"
	"github.com/bqckup/bqckup-go/internal/engine/restic/tree"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestRepository(t *testing.T) *repository.Repository {
	t.Helper()
	repo, err := repository.Init(context.Background(), backend.NewLocal(t.TempDir()), "restorer-test-password")
	require.NoError(t, err)
	return repo
}

// backupTree writes a source tree into the repository and returns the
// stored snapshot document.
func backupTree(t *testing.T, repo *repository.Repository, source string) snapshot.Snapshot {
	t.Helper()
	snapID, _, err := archiver.New(repo).Backup(context.Background(), archiver.BackupSpec{Paths: []string{source}})
	require.NoError(t, err)
	entry, err := repo.LoadSnapshot(context.Background(), snapID)
	require.NoError(t, err)
	return entry.Snapshot
}

// saveTree stores one hand-crafted tree blob.
func saveTree(t *testing.T, ctx context.Context, repo *repository.Repository, tr *tree.Tree) restic.ID {
	t.Helper()
	doc, err := tr.Marshal()
	require.NoError(t, err)
	id, err := repo.SaveBlob(ctx, restic.TreeBlob, doc)
	require.NoError(t, err)
	return id
}

func restoredPath(target, source, name string) string {
	return filepath.Join(target, strings.TrimLeft(source, "/"), name)
}

var proceed = func([]string) error { return nil }

func TestRestoreRoundTrip(t *testing.T) {
	repo := newTestRepository(t)
	source := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(source, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "hello.txt"), []byte("hello world"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(source, "sub", "nested.txt"), []byte("nested"), 0o600))
	require.NoError(t, os.Symlink("hello.txt", filepath.Join(source, "link")))
	require.NoError(t, os.MkdirAll(filepath.Join(source, "empty"), 0o750))
	snap := backupTree(t, repo, source)

	target := filepath.Join(t.TempDir(), "restore")
	summary, err := New(repo).Restore(context.Background(), snap, []string{source}, target, proceed)
	require.NoError(t, err)
	assert.Equal(t, 2, summary.FilesRestored)
	assert.Equal(t, int64(len("hello world")+len("nested")), summary.BytesRestored)
	assert.Empty(t, summary.SkippedPaths)

	hello := restoredPath(target, source, "hello.txt")
	data, err := os.ReadFile(hello)
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))
	info, err := os.Stat(hello)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	sourceInfo, err := os.Stat(filepath.Join(source, "hello.txt"))
	require.NoError(t, err)
	assert.Equal(t, sourceInfo.ModTime().Unix(), info.ModTime().Unix())

	nested := restoredPath(target, source, filepath.Join("sub", "nested.txt"))
	data, err = os.ReadFile(nested)
	require.NoError(t, err)
	assert.Equal(t, "nested", string(data))
	info, err = os.Stat(nested)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	linkTarget, err := os.Readlink(restoredPath(target, source, "link"))
	require.NoError(t, err)
	assert.Equal(t, "hello.txt", linkTarget)

	emptyInfo, err := os.Stat(restoredPath(target, source, "empty"))
	require.NoError(t, err)
	assert.True(t, emptyInfo.IsDir())
}

func TestRestoreFiltersToConfiguredPaths(t *testing.T) {
	repo := newTestRepository(t)
	one := t.TempDir()
	two := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(one, "one.txt"), []byte("one"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(two, "two.txt"), []byte("two"), 0o644))
	snapID, _, err := archiver.New(repo).Backup(context.Background(), archiver.BackupSpec{Paths: []string{one, two}})
	require.NoError(t, err)
	entry, err := repo.LoadSnapshot(context.Background(), snapID)
	require.NoError(t, err)

	target := filepath.Join(t.TempDir(), "restore")
	summary, err := New(repo).Restore(context.Background(), entry.Snapshot, []string{one}, target, proceed)
	require.NoError(t, err)
	_, err = os.Stat(restoredPath(target, one, "one.txt"))
	require.NoError(t, err)
	_, err = os.Stat(restoredPath(target, two, "two.txt"))
	assert.True(t, os.IsNotExist(err))
	assert.Equal(t, 1, summary.FilesRestored)
}

func TestRestoreReportsSkippedPaths(t *testing.T) {
	repo := newTestRepository(t)
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "f.txt"), []byte("x"), 0o644))
	snap := backupTree(t, repo, source)

	missing := "/not/in/snapshot"
	target := filepath.Join(t.TempDir(), "restore")
	summary, err := New(repo).Restore(context.Background(), snap, []string{source, missing}, target, proceed)
	require.NoError(t, err)
	assert.Equal(t, []string{missing}, summary.SkippedPaths)
}

func TestRestoreConfirmReceivesConflicts(t *testing.T) {
	repo := newTestRepository(t)
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "hello.txt"), []byte("snapshot content"), 0o644))
	snap := backupTree(t, repo, source)

	target := t.TempDir()
	conflictPath := restoredPath(target, source, "hello.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(conflictPath), 0o755))
	require.NoError(t, os.WriteFile(conflictPath, []byte("old content"), 0o644))

	var got []string
	_, err := New(repo).Restore(context.Background(), snap, []string{source}, target, func(conflicts []string) error {
		got = append(got, conflicts...)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{conflictPath}, got)
	data, err := os.ReadFile(conflictPath)
	require.NoError(t, err)
	assert.Equal(t, "snapshot content", string(data))
}

func TestRestoreConfirmErrorAborts(t *testing.T) {
	repo := newTestRepository(t)
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "hello.txt"), []byte("snapshot content"), 0o644))
	snap := backupTree(t, repo, source)

	target := t.TempDir()
	conflictPath := restoredPath(target, source, "hello.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(conflictPath), 0o755))
	require.NoError(t, os.WriteFile(conflictPath, []byte("old content"), 0o644))

	sentinel := errors.New("user said no")
	_, err := New(repo).Restore(context.Background(), snap, []string{source}, target, func([]string) error { return sentinel })
	require.ErrorIs(t, err, sentinel)

	data, err := os.ReadFile(conflictPath)
	require.NoError(t, err)
	assert.Equal(t, "old content", string(data))
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".bqckup-restore-*"))
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestRestoreOverwritesAfterConfirm(t *testing.T) {
	repo := newTestRepository(t)
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "hello.txt"), []byte("snapshot content"), 0o644))
	snap := backupTree(t, repo, source)

	target := t.TempDir()
	conflictPath := restoredPath(target, source, "hello.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(conflictPath), 0o755))
	require.NoError(t, os.WriteFile(conflictPath, []byte("old content"), 0o644))

	_, err := New(repo).Restore(context.Background(), snap, []string{source}, target, proceed)
	require.NoError(t, err)
	data, err := os.ReadFile(conflictPath)
	require.NoError(t, err)
	assert.Equal(t, "snapshot content", string(data))
}

func TestRestoreDoesNotFollowTargetSymlinks(t *testing.T) {
	for _, test := range []struct {
		name         string
		conflictPath func(target, source string) string
	}{
		{
			name: "snapshot directory",
			conflictPath: func(target, source string) string {
				return restoredPath(target, source, "node")
			},
		},
		{
			name: "synthetic ancestor",
			conflictPath: func(target, source string) string {
				return filepath.Join(target, strings.Split(strings.TrimLeft(filepath.Clean(source), string(filepath.Separator)), string(filepath.Separator))[0])
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newTestRepository(t)
			source := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(source, "node"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(source, "node", "child.txt"), []byte("restored"), 0o644))
			snap := backupTree(t, repo, source)

			target := t.TempDir()
			external := t.TempDir()
			conflict := test.conflictPath(target, source)
			require.NoError(t, os.MkdirAll(filepath.Dir(conflict), 0o755))
			require.NoError(t, os.Symlink(external, conflict))

			_, err := New(repo).Restore(context.Background(), snap, []string{source}, target, proceed)
			require.NoError(t, err)
			info, err := os.Lstat(conflict)
			require.NoError(t, err)
			assert.True(t, info.IsDir())
			assert.Zero(t, info.Mode()&os.ModeSymlink)
			externalEntries, err := os.ReadDir(external)
			require.NoError(t, err)
			assert.Empty(t, externalEntries, "restore wrote outside the target")
			data, err := os.ReadFile(restoredPath(target, source, filepath.Join("node", "child.txt")))
			require.NoError(t, err)
			assert.Equal(t, "restored", string(data))
		})
	}
}

func TestRestoreDecliningSymlinkConflictLeavesTargetUntouched(t *testing.T) {
	repo := newTestRepository(t)
	source := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(source, "node"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "node", "child.txt"), []byte("restored"), 0o644))
	snap := backupTree(t, repo, source)

	target := t.TempDir()
	external := t.TempDir()
	firstComponent := strings.Split(strings.TrimLeft(filepath.Clean(source), string(filepath.Separator)), string(filepath.Separator))[0]
	conflict := filepath.Join(target, firstComponent)
	require.NoError(t, os.Symlink(external, conflict))
	sentinel := errors.New("user declined overwrite")
	var got []string
	_, err := New(repo).Restore(context.Background(), snap, []string{source}, target, func(conflicts []string) error {
		got = append(got, conflicts...)
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, []string{conflict}, got)
	info, err := os.Lstat(conflict)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)
	externalEntries, err := os.ReadDir(external)
	require.NoError(t, err)
	assert.Empty(t, externalEntries)
}

func TestRestoreReplacesConflictingPathTypes(t *testing.T) {
	t.Run("directory over file", func(t *testing.T) {
		repo := newTestRepository(t)
		source := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(source, "node"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(source, "node", "child.txt"), []byte("restored"), 0o644))
		snap := backupTree(t, repo, source)

		target := t.TempDir()
		conflict := restoredPath(target, source, "node")
		require.NoError(t, os.MkdirAll(filepath.Dir(conflict), 0o755))
		require.NoError(t, os.WriteFile(conflict, []byte("old file"), 0o644))

		_, err := New(repo).Restore(context.Background(), snap, []string{source}, target, proceed)
		require.NoError(t, err)
		data, err := os.ReadFile(filepath.Join(conflict, "child.txt"))
		require.NoError(t, err)
		assert.Equal(t, "restored", string(data))
	})

	t.Run("file over directory", func(t *testing.T) {
		repo := newTestRepository(t)
		source := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(source, "node"), []byte("restored"), 0o644))
		snap := backupTree(t, repo, source)

		target := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(target, "unrelated.txt"), []byte("keep"), 0o644))
		conflict := restoredPath(target, source, "node")
		require.NoError(t, os.MkdirAll(conflict, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(conflict, "old.txt"), []byte("old"), 0o644))

		_, err := New(repo).Restore(context.Background(), snap, []string{source}, target, proceed)
		require.NoError(t, err)
		data, err := os.ReadFile(conflict)
		require.NoError(t, err)
		assert.Equal(t, "restored", string(data))
		unrelated, err := os.ReadFile(filepath.Join(target, "unrelated.txt"))
		require.NoError(t, err)
		assert.Equal(t, "keep", string(unrelated))
	})

	t.Run("symlink over directory", func(t *testing.T) {
		repo := newTestRepository(t)
		source := t.TempDir()
		require.NoError(t, os.Symlink("destination.txt", filepath.Join(source, "node")))
		snap := backupTree(t, repo, source)

		target := t.TempDir()
		conflict := restoredPath(target, source, "node")
		require.NoError(t, os.MkdirAll(conflict, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(conflict, "old.txt"), []byte("old"), 0o644))

		_, err := New(repo).Restore(context.Background(), snap, []string{source}, target, proceed)
		require.NoError(t, err)
		linkTarget, err := os.Readlink(conflict)
		require.NoError(t, err)
		assert.Equal(t, "destination.txt", linkTarget)
	})
}

func TestRestoreRejectsUnsafeSnapshotTree(t *testing.T) {
	t.Run("symlink with subtree", func(t *testing.T) {
		ctx := context.Background()
		repo := newTestRepository(t)
		external := t.TempDir()
		subID := saveTree(t, ctx, repo, &tree.Tree{Nodes: []*tree.Node{
			{Name: "pwn", Type: tree.TypeFile, Mode: 0o644, Content: []restic.ID{}},
		}})
		rootID := saveTree(t, ctx, repo, &tree.Tree{Nodes: []*tree.Node{
			{Name: "data", Type: tree.TypeSymlink, LinkTarget: external, Subtree: &subID},
		}})
		require.NoError(t, repo.Flush(ctx))
		snapID, err := repo.SaveSnapshot(ctx, snapshot.Snapshot{Tree: &rootID, Paths: []string{"/data"}})
		require.NoError(t, err)
		entry, err := repo.LoadSnapshot(ctx, snapID)
		require.NoError(t, err)

		_, err = New(repo).Restore(ctx, entry.Snapshot, []string{"/data"}, filepath.Join(t.TempDir(), "restore"), proceed)
		require.Error(t, err)
		externalEntries, readErr := os.ReadDir(external)
		require.NoError(t, readErr)
		assert.Empty(t, externalEntries, "malformed snapshot escaped staging")
	})

	t.Run("name with path separator", func(t *testing.T) {
		ctx := context.Background()
		repo := newTestRepository(t)
		subID := saveTree(t, ctx, repo, &tree.Tree{Nodes: []*tree.Node{
			{Name: "nested/pwn", Type: tree.TypeFile, Mode: 0o644, Content: []restic.ID{}},
		}})
		rootID := saveTree(t, ctx, repo, &tree.Tree{Nodes: []*tree.Node{
			{Name: "data", Type: tree.TypeDir, Mode: 0o755, Subtree: &subID},
		}})
		require.NoError(t, repo.Flush(ctx))
		snapID, err := repo.SaveSnapshot(ctx, snapshot.Snapshot{Tree: &rootID, Paths: []string{"/data"}})
		require.NoError(t, err)
		entry, err := repo.LoadSnapshot(ctx, snapID)
		require.NoError(t, err)

		_, err = New(repo).Restore(ctx, entry.Snapshot, []string{"/data"}, filepath.Join(t.TempDir(), "restore"), proceed)
		require.ErrorContains(t, err, "invalid name")
	})
}

func TestRestoreCancellationRemovesStaging(t *testing.T) {
	repo := newTestRepository(t)
	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "hello.txt"), []byte("snapshot content"), 0o644))
	snap := backupTree(t, repo, source)

	target := t.TempDir()
	conflictPath := restoredPath(target, source, "hello.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(conflictPath), 0o755))
	require.NoError(t, os.WriteFile(conflictPath, []byte("old content"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	_, err := New(repo).Restore(ctx, snap, []string{source}, target, func([]string) error {
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)

	data, err := os.ReadFile(conflictPath)
	require.NoError(t, err)
	assert.Equal(t, "old content", string(data))
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".bqckup-restore-*"))
	require.NoError(t, err)
	assert.Empty(t, matches)
}

func TestRestoreSkipsSpecialNodes(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	subID := saveTree(t, ctx, repo, &tree.Tree{Nodes: []*tree.Node{
		{Name: "f.txt", Type: tree.TypeFile, Mode: 0o644, Content: []restic.ID{}},
		{Name: "pipe", Type: tree.TypeFIFO, Mode: 0o600},
	}})
	rootID := saveTree(t, ctx, repo, &tree.Tree{Nodes: []*tree.Node{
		{Name: "data", Type: tree.TypeDir, Mode: 0o755, Subtree: &subID},
	}})
	require.NoError(t, repo.Flush(ctx))
	snapID, err := repo.SaveSnapshot(ctx, snapshot.Snapshot{Tree: &rootID, Paths: []string{"/data"}})
	require.NoError(t, err)
	entry, err := repo.LoadSnapshot(ctx, snapID)
	require.NoError(t, err)

	target := filepath.Join(t.TempDir(), "restore")
	summary, err := New(repo).Restore(ctx, entry.Snapshot, []string{"/data"}, target, proceed)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.FilesRestored)
	_, err = os.Stat(filepath.Join(target, "data", "f.txt"))
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(target, "data", "pipe"))
	assert.True(t, os.IsNotExist(err))
}

func TestRestoreMissingBlobFails(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepository(t)
	missing := restic.Hash([]byte("never stored"))
	subID := saveTree(t, ctx, repo, &tree.Tree{Nodes: []*tree.Node{
		{Name: "f.txt", Type: tree.TypeFile, Mode: 0o644, Content: []restic.ID{missing}},
	}})
	rootID := saveTree(t, ctx, repo, &tree.Tree{Nodes: []*tree.Node{
		{Name: "data", Type: tree.TypeDir, Mode: 0o755, Subtree: &subID},
	}})
	require.NoError(t, repo.Flush(ctx))
	snapID, err := repo.SaveSnapshot(ctx, snapshot.Snapshot{Tree: &rootID, Paths: []string{"/data"}})
	require.NoError(t, err)
	entry, err := repo.LoadSnapshot(ctx, snapID)
	require.NoError(t, err)

	target := filepath.Join(t.TempDir(), "restore")
	_, err = New(repo).Restore(ctx, entry.Snapshot, []string{"/data"}, target, proceed)
	require.Error(t, err)
	_, err = os.Stat(target)
	assert.True(t, os.IsNotExist(err))
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), ".bqckup-restore-*"))
	require.NoError(t, err)
	assert.Empty(t, matches)
}
