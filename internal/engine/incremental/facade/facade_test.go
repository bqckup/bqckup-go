package facade

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	backupincremental "github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/backend"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/lock"
	"github.com/bqckup/bqckup-go/internal/engine/incremental/repository"
)

const testPassword = "facade-test-password"

func testRepo(t *testing.T) backupincremental.RepoConfig {
	t.Helper()
	return backupincremental.RepoConfig{URL: t.TempDir(), Password: testPassword}
}

// listSnapshots opens the repository below the facade and lists snapshots.
func listSnapshots(t *testing.T, repo backupincremental.RepoConfig) []repository.SnapshotWithID {
	t.Helper()
	r, err := repository.Open(context.Background(), backend.NewLocal(repo.URL), repo.Password)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return snapshots
}

func TestEnsureAndBackupAndList(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)

	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	// idempotent
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("facade data"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := engine.BackupFiles(ctx, repo, backupincremental.BackupSpec{
		SiteName: "testsite",
		Include:  []string{source},
		Tags:     []string{"bqckup", "site:testsite"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.SnapshotID == "" || summary.TotalFilesProcessed != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	snapshots := listSnapshots(t, repo)
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
}

func TestBackupNeedsNoResticBinary(t *testing.T) {
	// The facade path spawns no processes: a full run must work with a PATH
	// that contains only a directory without incremental.
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", t.TempDir()) // empty PATH: no binaries at all
	defer os.Setenv("PATH", oldPath)

	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.BackupFiles(ctx, repo, backupincremental.BackupSpec{Include: []string{source}, Tags: []string{"t"}}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRetentionKeepsNewest(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// three backups: newest must survive, oldest two are deleted (keep 2
	// only deletes 1; use keep 1 to delete 2) — use keepLast=2, delete 1
	source := t.TempDir()
	for i := 0; i < 3; i++ {
		file := filepath.Join(source, "f.txt")
		if err := os.WriteFile(file, []byte(strings.Repeat("x", i+1)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.BackupFiles(ctx, repo, backupincremental.BackupSpec{
			Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.ApplyRetention(ctx, repo, 2, "testsite"); err != nil {
		t.Fatal(err)
	}
	snapshots := listSnapshots(t, repo)
	if len(snapshots) != 2 {
		t.Fatalf("after retention: %d snapshots, want 2", len(snapshots))
	}
}

func TestApplyRetentionIgnoresOtherSites(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// one snapshot for another site
	if _, err := engine.BackupFiles(ctx, repo, backupincremental.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:othersite"},
	}); err != nil {
		t.Fatal(err)
	}
	// retention for THIS site must not touch the foreign snapshot
	if _, err := engine.ApplyRetention(ctx, repo, 1, "testsite"); err != nil {
		t.Fatal(err)
	}
	snapshots := listSnapshots(t, repo)
	if len(snapshots) != 1 {
		t.Fatalf("foreign snapshot was deleted: %d remain", len(snapshots))
	}
}

func TestRejectsRemoteURLs(t *testing.T) {
	engine := NewEngine()
	repo := backupincremental.RepoConfig{URL: "rest:https://user:secret-key@example.com/repo", Password: "x"}
	err := engine.EnsureRepository(context.Background(), repo)
	if err == nil {
		t.Fatal("want error for rest URL")
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatal("error leaks URL credentials")
	}
}

func TestUnlockSucceedsOnCleanRepository(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := engine.Unlock(ctx, repo); err != nil {
		t.Fatalf("Unlock on a repository without locks must succeed, got %v", err)
	}
}

// compile-time check: the facade satisfies the runner's interface.
var _ interface {
	EnsureRepository(context.Context, backupincremental.RepoConfig) error
	BackupFiles(context.Context, backupincremental.RepoConfig, backupincremental.BackupSpec) (backupincremental.SnapshotSummary, error)
	ApplyRetention(context.Context, backupincremental.RepoConfig, int, string) (int64, error)
	Unlock(context.Context, backupincremental.RepoConfig) error
} = (*Engine)(nil)

func TestListSnapshotsListsRepositorySnapshotsAndReleasesLock(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	// Empty repository: empty slice, not an error.
	snapshots, err := engine.ListSnapshots(ctx, repo)
	if err != nil {
		t.Fatalf("ListSnapshots on empty repository: %v", err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("empty repository returned %d snapshots", len(snapshots))
	}

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("snapshot data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.BackupFiles(ctx, repo, backupincremental.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
	}); err != nil {
		t.Fatal(err)
	}

	snapshots, err = engine.ListSnapshots(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snapshots))
	}
	listed := snapshots[0]
	if len(listed.ID) != 64 {
		t.Fatalf("snapshot ID %q is not a full hash", listed.ID)
	}
	if len(listed.Paths) != 1 || listed.Paths[0] != source {
		t.Fatalf("paths = %v, want [%s]", listed.Paths, source)
	}
	if listed.Size <= 0 {
		t.Fatalf("size = %d, want > 0", listed.Size)
	}
	if time.Since(listed.CreatedAt) > time.Minute {
		t.Fatalf("created at %v is not recent", listed.CreatedAt)
	}

	// The non-exclusive lock must be gone after listing.
	b := backend.NewLocal(repo.URL)
	var locks []incremental.Handle
	if err := b.List(ctx, incremental.LockFile, func(handle incremental.Handle, _ int64) error {
		locks = append(locks, handle)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(locks) != 0 {
		t.Fatalf("listing left %d lock files behind", len(locks))
	}
}

func TestListSnapshotsTakesANonExclusiveLockDuringListing(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	b := backend.NewLocal(repo.URL)

	// Hold a non-exclusive lock, then list: listing must not conflict.
	opened, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := lock.New(ctx, b, opened.MasterKey(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ListSnapshots(ctx, repo); err != nil {
		t.Fatalf("listing alongside a non-exclusive lock: %v", err)
	}
	if err := existing.Unlock(ctx, b); err != nil {
		t.Fatal(err)
	}
}

// TestEnsureRepositoryRefusesToInitWhileAnotherMachineHoldsTheInitLock:
// first-run init is stat-then-write; without serialization, two machines
// starting the first backup of one site simultaneously would each write a
// config with its own random master key and the loser's data would be
// undecryptable.
func TestEnsureRepositoryRefusesToInitWhileAnotherMachineHoldsTheInitLock(t *testing.T) {
	ctx := context.Background()
	repo := testRepo(t)
	b := backend.NewLocal(repo.URL)
	if err := b.CreateLayout(); err != nil {
		t.Fatal(err)
	}

	other, err := lock.New(ctx, b, initLockKey(repo.Password), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Unlock(ctx, b) }()

	err = NewEngine().EnsureRepository(ctx, repo)
	var locked *lock.ErrLocked
	if !errors.As(err, &locked) {
		t.Fatalf("EnsureRepository under a concurrent init lock: got %v, want ErrLocked", err)
	}
	// The losing machine must not have written a competing config.
	if _, statErr := b.Stat(ctx, incremental.Handle{Type: incremental.ConfigFile}); !b.IsNotExist(statErr) {
		t.Fatalf("refused init wrote a config: %v", statErr)
	}
}

// TestEnsureRepositoryLeavesNoInitLockBehind guards the success and error
// paths: a completed (or refused) initialization must not leave lock files
// that block the first real backup.
func TestEnsureRepositoryLeavesNoInitLockBehind(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	b := backend.NewLocal(repo.URL)
	var locks []incremental.Handle
	if err := b.List(ctx, incremental.LockFile, func(handle incremental.Handle, _ int64) error {
		locks = append(locks, handle)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(locks) != 0 {
		t.Fatalf("EnsureRepository left %d lock files behind", len(locks))
	}
}

func TestRestoreRoundTripLocalRepository(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("restore me"), 0o640); err != nil {
		t.Fatal(err)
	}
	summary, err := engine.BackupFiles(ctx, repo, backupincremental.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
	})
	if err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "restore")
	restored, err := engine.RestoreSnapshot(ctx, repo, summary.SnapshotID, []string{source}, target, func([]string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if restored.FilesRestored != 1 || restored.SnapshotID != summary.SnapshotID || restored.Target != target {
		t.Fatalf("unexpected summary: %+v", restored)
	}
	restoredPath := filepath.Join(target, strings.TrimLeft(source, "/"), "data.txt")
	data, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "restore me" {
		t.Fatalf("restored content = %q", data)
	}
	info, err := os.Stat(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("restored mode = %v, want 0640", info.Mode().Perm())
	}

	// The non-exclusive lock must be gone after restoring.
	b := backend.NewLocal(repo.URL)
	var locks []incremental.Handle
	if err := b.List(ctx, incremental.LockFile, func(handle incremental.Handle, _ int64) error {
		locks = append(locks, handle)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(locks) != 0 {
		t.Fatalf("restore left %d lock files behind", len(locks))
	}
}

func TestRestoreRejectsUnsupportedURL(t *testing.T) {
	engine := NewEngine()
	repo := backupincremental.RepoConfig{URL: "sftp:host/repo", Password: "x"}
	_, err := engine.RestoreSnapshot(context.Background(), repo, strings.Repeat("a", 64), []string{"/a"}, "/tmp/restore", func([]string) error { return nil })
	if err == nil {
		t.Fatal("want error for sftp URL")
	}
	if !strings.Contains(err.Error(), "does not support sftp:") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRestoreUnknownSnapshotFails(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	_, err := engine.RestoreSnapshot(ctx, repo, strings.Repeat("f", 64), []string{"/a"}, filepath.Join(t.TempDir(), "restore"), func([]string) error { return nil })
	if err == nil {
		t.Fatal("want error for unknown snapshot")
	}
	if !strings.Contains(err.Error(), "could not load the snapshot") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRestoreTakesNonExclusiveLockDuringRestore(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	summary, err := engine.BackupFiles(ctx, repo, backupincremental.BackupSpec{
		Include: []string{source}, Tags: []string{"site:testsite"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b := backend.NewLocal(repo.URL)
	opened, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := lock.New(ctx, b, opened.MasterKey(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.RestoreSnapshot(ctx, repo, summary.SnapshotID, []string{source}, filepath.Join(t.TempDir(), "restore"), func([]string) error { return nil }); err != nil {
		t.Fatalf("restore alongside a non-exclusive lock: %v", err)
	}
	if err := existing.Unlock(ctx, b); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRepositoryHealthyAndLockReleased(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("check me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := engine.CheckRepository(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "healthy" || len(result.Findings) != 0 {
		t.Fatalf("healthy repository: %+v", result)
	}
	if result.Indexes == 0 || result.Snapshots != 1 || result.Packs == 0 || result.Blobs == 0 {
		t.Fatalf("healthy repository counts are empty: %+v", result)
	}
	// read-data mode stays clean too
	result, err = engine.CheckRepository(ctx, repo, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "healthy" || len(result.Findings) != 0 {
		t.Fatalf("healthy read-data check: %+v", result)
	}
	// the non-exclusive lock is released on every path: a new backup works
	if _, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
	}); err != nil {
		t.Fatalf("backup after check: %v", err)
	}
}

func TestCheckRepositoryReportsCorruptionWithoutLockingOut(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("check me too"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
	}); err != nil {
		t.Fatal(err)
	}

	// corrupt the config file in place
	b := backend.NewLocal(repo.URL)
	var raw []byte
	if err := b.Load(ctx, restic.Handle{Type: restic.ConfigFile}, 0, 0, func(rd io.Reader) error {
		var err error
		raw, err = io.ReadAll(rd)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw[5] ^= 0xff
	if err := b.Save(ctx, restic.Handle{Type: restic.ConfigFile}, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}

	result, err := engine.CheckRepository(ctx, repo, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "problems" {
		t.Fatalf("corrupt repository status = %q, want problems", result.Status)
	}
	broken := false
	for _, finding := range result.Findings {
		if finding.Type == "broken_config" && finding.ID == "config" {
			broken = true
		}
	}
	if !broken {
		t.Fatalf("want broken_config finding, got %+v", result.Findings)
	}
	// no lock is left behind that would block the next backup
	var locks []restic.Handle
	if err := b.List(ctx, restic.LockFile, func(handle restic.Handle, _ int64) error {
		locks = append(locks, handle)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(locks) != 0 {
		t.Fatalf("check left %d lock files behind", len(locks))
	}
}

func TestCheckRepositoryRejectsUnsupportedURL(t *testing.T) {
	engine := NewEngine()
	repo := backuprestic.RepoConfig{URL: "b2:repo", Password: "x"}
	_, err := engine.CheckRepository(context.Background(), repo, false)
	if err == nil || !strings.Contains(err.Error(), "does not support b2:") {
		t.Fatalf("want unsupported URL error, got %v", err)
	}
}

func TestFacadeRepairIndexRebuildsValidIndex(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "repair_me.txt"), []byte("repair index facade data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.BackupFiles(ctx, repo, backuprestic.BackupSpec{
		Include: []string{source}, Tags: []string{"bqckup", "site:testsite"},
	}); err != nil {
		t.Fatal(err)
	}

	// Delete all index files directly from backend
	b := backend.NewLocal(repo.URL)
	var indexes []restic.Handle
	if err := b.List(ctx, restic.IndexFile, func(h restic.Handle, _ int64) error {
		indexes = append(indexes, h)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, h := range indexes {
		if err := b.Remove(ctx, h); err != nil {
			t.Fatal(err)
		}
	}

	result, err := engine.RepairIndex(ctx, repo)
	if err != nil {
		t.Fatalf("RepairIndex failed: %v", err)
	}
	if result.PacksProcessed == 0 || result.BlobsIndexed == 0 || result.NewIndexesWritten != 1 {
		t.Fatalf("unexpected repair result: %+v", result)
	}

	// Verify check passes after repair
	checkResult, err := engine.CheckRepository(ctx, repo, true)
	if err != nil {
		t.Fatalf("CheckRepository failed: %v", err)
	}
	if checkResult.Status != "healthy" || len(checkResult.Findings) != 0 {
		t.Fatalf("check result after repair not healthy: %+v", checkResult)
	}

	// Verify snapshots can be listed
	snaps, err := engine.ListSnapshots(ctx, repo)
	if err != nil {
		t.Fatalf("ListSnapshots failed: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots count = %d, want 1", len(snaps))
	}
}

func TestFacadeRepairIndexLockConflict(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine()
	repo := testRepo(t)
	if err := engine.EnsureRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}

	b := backend.NewLocal(repo.URL)
	r, err := repository.Open(ctx, b, repo.Password)
	if err != nil {
		t.Fatal(err)
	}

	// Hold an exclusive lock
	activeLock, err := lock.New(ctx, b, r.MasterKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = activeLock.Unlock(context.WithoutCancel(ctx), b) }()

	// RepairIndex should fail due to lock conflict
	_, err = engine.RepairIndex(ctx, repo)
	if err == nil {
		t.Fatal("expected error on RepairIndex while repository is locked, got nil")
	}
}
