package archiver

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/repository"
)

const testPassword = "archiver-test-password"

// buildDataset creates the acceptance dataset: files, subdirs, a symlink,
// an empty file, and one large file.
func buildDataset(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "hello.txt"), []byte("hello world\n"))
	writeFile(t, filepath.Join(dir, "empty.txt"), nil)
	writeFile(t, filepath.Join(dir, "sub", "nested.txt"), []byte("nested content"))
	big := make([]byte, 20*1024*1024) // multi-chunk (> 8 MiB max chunk)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "big.bin"), big)
	if err := os.Symlink("hello.txt", filepath.Join(dir, "link-to-hello")); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func openRepo(t *testing.T, ctx context.Context, local *backend.Local) *repository.Repository {
	t.Helper()
	repo, err := repository.Open(ctx, local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func newArchiver(t *testing.T, ctx context.Context) (*Archiver, *backend.Local, string) {
	t.Helper()
	local := backend.NewLocal(t.TempDir())
	if _, err := repository.Init(ctx, local, testPassword); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(ctx, local, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return New(repo), local, t.TempDir()
}

func TestBackupWritesSnapshot(t *testing.T) {
	ctx := context.Background()
	arch, local, source := newArchiver(t, ctx)
	buildDataset(t, source)

	snapID, summary, err := arch.Backup(ctx, BackupSpec{
		Paths:    []string{source},
		Tags:     []string{"bqckup", "site:test"},
		Hostname: "test-host",
		Username: "test-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapID.IsNull() {
		t.Fatal("snapshot id is null")
	}
	if summary.TotalFilesProcessed != 4 {
		t.Fatalf("files processed = %d, want 4", summary.TotalFilesProcessed)
	}
	if summary.DataAdded == 0 {
		t.Fatal("data added is 0")
	}
	if summary.SnapshotID != snapID.String() {
		t.Fatal("summary snapshot id mismatch")
	}

	repo := openRepo(t, ctx, local)
	snapshots, err := repo.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
	snap := snapshots[0].Snapshot
	if snap.Tree == nil || snap.Tree.IsNull() {
		t.Fatal("snapshot tree is missing")
	}
	if len(snap.Paths) != 1 || snap.Paths[0] != source {
		t.Fatalf("snapshot paths = %v", snap.Paths)
	}
	if len(snap.Tags) != 2 || snap.Tags[1] != "site:test" {
		t.Fatalf("snapshot tags = %v", snap.Tags)
	}
	if snap.Hostname != "test-host" || snap.Username != "test-user" {
		t.Fatalf("snapshot identity = %s@%s", snap.Username, snap.Hostname)
	}
}

func TestSecondBackupDedups(t *testing.T) {
	ctx := context.Background()
	arch, local, source := newArchiver(t, ctx)
	buildDataset(t, source)

	first, _, err := arch.Backup(ctx, BackupSpec{Paths: []string{source}})
	if err != nil {
		t.Fatal(err)
	}
	_ = first

	repo := openRepo(t, ctx, local)
	arch = New(repo)
	_, secondSummary, err := arch.Backup(ctx, BackupSpec{Paths: []string{source}})
	if err != nil {
		t.Fatal(err)
	}
	// identical data: zero new data blobs (spec §9.4 dedup gate)
	if secondSummary.DataAdded != 0 {
		detail := ""
		for _, m := range secondSummary.Missed {
			detail += fmt.Sprintf(" [type=%d size=%d]", m.Type, m.Size)
		}
		var idxFiles string
		if err := local.List(ctx, incremental.IndexFile, func(h incremental.Handle, size int64) error {
			idxFiles += fmt.Sprintf(" %s:%d", h.Name[:8], size)
			return nil
		}); err != nil {
			idxFiles = " list-error:" + err.Error()
		}
		t.Fatalf("second backup added %d bytes, want 0; missed blobs:%s; index entries=%d; index files:%s",
			secondSummary.DataAdded, detail, repo.MasterIndex().Len(), idxFiles)
	}
	snapshots, err := repo.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want 2", len(snapshots))
	}
}

func TestSecondBackupReusesUnmodifiedFiles(t *testing.T) {
	ctx := context.Background()
	arch, local, source := newArchiver(t, ctx)
	buildDataset(t, source)
	spec := BackupSpec{
		Paths:    []string{source},
		Excludes: []string{"*.tmp"},
		Tags:     []string{"site:test"},
	}
	firstID, _, err := arch.Backup(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}

	repo := openRepo(t, ctx, local)
	_, summary, err := New(repo).Backup(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesNew != 0 {
		t.Fatalf("files new = %d, want 0", summary.FilesNew)
	}
	if summary.FilesChanged != 0 {
		t.Fatalf("files changed = %d, want 0", summary.FilesChanged)
	}
	if summary.FilesUnmodified != 4 {
		t.Fatalf("files unmodified = %d, want 4", summary.FilesUnmodified)
	}

	snapshots, err := repo.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("snapshot count = %d, want 2", len(snapshots))
	}
	var parentFound bool
	for _, entry := range snapshots {
		if entry.Snapshot.Parent != nil && *entry.Snapshot.Parent == firstID {
			parentFound = true
			break
		}
	}
	if !parentFound {
		t.Fatalf("second snapshot parent = %#v, want previous snapshot", snapshots)
	}
}

func TestNodeForDoesNotRecordAtime(t *testing.T) {
	// atime changes when a backup reads the file (relatime filesystems), so
	// recording it would make every tree differ on the next run and break
	// dedup. The archiver must never store it.
	ctx := context.Background()
	arch, _, source := newArchiver(t, ctx)
	path := filepath.Join(source, "f.txt")
	writeFile(t, path, []byte("x"))
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	state := &backupState{archiver: arch, spec: BackupSpec{Paths: []string{source}}}
	node, err := state.nodeFor(ctx, path, info)
	if err != nil {
		t.Fatal(err)
	}
	if !node.AccessTime.IsZero() {
		t.Fatalf("node records atime %v; atime must never be stored", node.AccessTime)
	}
}

func TestOneByteChangeAddsOnlyAffectedChunks(t *testing.T) {
	ctx := context.Background()
	arch, local, source := newArchiver(t, ctx)
	buildDataset(t, source)
	if _, _, err := arch.Backup(ctx, BackupSpec{Paths: []string{source}}); err != nil {
		t.Fatal(err)
	}

	// change one byte of the large file: only the chunks covering it are new
	bigPath := filepath.Join(source, "big.bin")
	data, err := os.ReadFile(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(bigPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	repo := openRepo(t, ctx, local)
	_, summary, err := New(repo).Backup(ctx, BackupSpec{Paths: []string{source}})
	if err != nil {
		t.Fatal(err)
	}
	// one byte in the middle realigns at most a few 1 MiB chunks, never the
	// whole 20 MiB file
	if summary.DataAdded <= 0 {
		t.Fatal("changed byte did not produce new data")
	}
	if summary.DataAdded >= int64(len(data)) {
		t.Fatalf("one-byte change re-added the whole file: %d bytes", summary.DataAdded)
	}
	if summary.FilesChanged != 1 || summary.FilesUnmodified != 3 {
		t.Fatalf("summary = %#v, want one changed and three unmodified files", summary)
	}
}

func TestExcludes(t *testing.T) {
	ctx := context.Background()
	arch, local, source := newArchiver(t, ctx)
	buildDataset(t, source)
	writeFile(t, filepath.Join(source, "skip.tmp"), []byte("temporary"))
	writeFile(t, filepath.Join(source, "cache", "deep", "secret.bin"), []byte("excluded recursively"))

	_, summary, err := arch.Backup(ctx, BackupSpec{
		Paths:    []string{source},
		Excludes: []string{"*.tmp", "cache/**"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalFilesProcessed != 4 {
		t.Fatalf("files processed = %d, want 4 (skip.tmp excluded)", summary.TotalFilesProcessed)
	}
	_ = local
}

func TestCancellationLeavesConsistentRepo(t *testing.T) {
	ctx := context.Background()
	arch, local, source := newArchiver(t, ctx)
	buildDataset(t, source)

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err := arch.Backup(cancelCtx, BackupSpec{Paths: []string{source}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	// no snapshot may exist after a cancelled run
	repo := openRepo(t, ctx, local)
	snapshots, err := repo.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("cancelled run left %d snapshots", len(snapshots))
	}
	// the repository must still open and accept a full backup afterwards
	if _, _, err := New(repo).Backup(ctx, BackupSpec{Paths: []string{source}}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupMultipleRootsWithSameBasename(t *testing.T) {
	ctx := context.Background()
	arch, local, _ := newArchiver(t, ctx)

	dirA := t.TempDir()
	dirB := t.TempDir()
	writeFile(t, filepath.Join(dirA, "data", "a.txt"), []byte("a"))
	writeFile(t, filepath.Join(dirB, "data", "b.txt"), []byte("b"))

	snapID, _, err := arch.Backup(ctx, BackupSpec{
		Paths: []string{filepath.Join(dirA, "data"), filepath.Join(dirB, "data")},
	})
	if err != nil {
		t.Fatalf("two roots with the same basename must back up: %v", err)
	}
	if snapID.IsNull() {
		t.Fatal("snapshot id is null")
	}
	repo := openRepo(t, ctx, local)
	snapshots, err := repo.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].Snapshot.Tree == nil {
		t.Fatalf("unexpected snapshots: %#v", snapshots)
	}
	if _, ok := repo.MasterIndex().Lookup(incremental.TreeBlob, *snapshots[0].Snapshot.Tree); !ok {
		t.Fatal("snapshot root tree was not persisted in any pack/index")
	}
}

func TestSecondBackupReusesMultipleRoots(t *testing.T) {
	ctx := context.Background()
	arch, local, _ := newArchiver(t, ctx)
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeFile(t, filepath.Join(dirA, "a.txt"), []byte("a"))
	writeFile(t, filepath.Join(dirB, "b.txt"), []byte("b"))
	spec := BackupSpec{Paths: []string{dirA, dirB}}
	if _, _, err := arch.Backup(ctx, spec); err != nil {
		t.Fatal(err)
	}
	repo := openRepo(t, ctx, local)
	_, summary, err := New(repo).Backup(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesNew != 0 || summary.FilesChanged != 0 || summary.FilesUnmodified != 2 {
		t.Fatalf("summary = %#v, want two unmodified files", summary)
	}
}

func TestBackupRejectsInvalidExcludePattern(t *testing.T) {
	ctx := context.Background()
	arch, _, source := newArchiver(t, ctx)
	writeFile(t, filepath.Join(source, "keep.txt"), []byte("keep"))

	_, _, err := arch.Backup(ctx, BackupSpec{Paths: []string{source}, Excludes: []string{"["}})
	if err == nil || !strings.Contains(err.Error(), "invalid exclude pattern") {
		t.Fatalf("want invalid-exclude-pattern error, got %v", err)
	}
}

func TestBackupEmptyDir(t *testing.T) {
	ctx := context.Background()
	arch, _, source := newArchiver(t, ctx)
	empty := filepath.Join(source, "emptydir")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := arch.Backup(ctx, BackupSpec{Paths: []string{empty}}); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRejectsNoPaths(t *testing.T) {
	ctx := context.Background()
	arch, _, _ := newArchiver(t, ctx)
	if _, _, err := arch.Backup(ctx, BackupSpec{}); err == nil {
		t.Fatal("want error for empty path list")
	}
}
