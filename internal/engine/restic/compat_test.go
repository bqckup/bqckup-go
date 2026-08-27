//go:build restic_compat

// Package restic_test runs the compatibility gate against the official
// restic binary: a repository created by this engine must pass
// restic check, restic snapshots, and restic restore.
//
// Skip rules (never fail):
//   - restic not found in PATH -> skip
//   - restic < 0.17.0 (repository format v1 only) -> skip
//   - restic >= 0.17.0 -> tests run and failures are real
//
// Run: go test -race -tags=restic_compat ./internal/engine/restic/...
package restic_test

import (
	"context"
	"crypto/rand"
	"encoding/json"

	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/engine/restic/archiver"
	"github.com/bqckup/bqckup-go/internal/engine/restic/backend"
	"github.com/bqckup/bqckup-go/internal/engine/restic/repository"
)

const compatPassword = "compat-test-password"

var resticBin = func() string {
	path, err := exec.LookPath("restic")
	if err != nil {
		return ""
	}
	return path
}()

// resticVersionOK reports whether the binary understands format v2.
func resticVersionOK(t *testing.T) bool {
	t.Helper()
	if resticBin == "" {
		t.Skip("restic binary not found in PATH (install restic >= 0.17.0 to run compat tests)")
	}
	out, err := exec.Command(resticBin, "version").Output()
	if err != nil {
		t.Fatalf("restic version failed: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		t.Fatalf("unexpected restic version output: %q", out)
	}
	parts := strings.SplitN(fields[1], ".", 3)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		t.Fatalf("unexpected restic version %q", fields[1])
	}
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if major < 0 || (major == 0 && minor < 17) {
		t.Skipf("restic %s is repository format v1 only; need >= 0.17.0", fields[1])
	}
	return true
}

func runRestic(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	fullArgs := append([]string{"--repo", repo, "--no-cache"}, args...)
	command := exec.Command(resticBin, fullArgs...)
	command.Env = append(os.Environ(), "RESTIC_PASSWORD="+compatPassword)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("restic %v failed: %v\n%s", fullArgs, err, output)
	}
	return output
}

func buildDataset(t *testing.T, dir string) {
	t.Helper()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("hello.txt", []byte("hello world from the pure-Go engine\n"))
	write("empty.txt", nil)
	write("sub/nested.txt", []byte("nested content\n"))
	big := make([]byte, 20*1024*1024)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	write("big.bin", big)
	if err := os.Symlink("hello.txt", filepath.Join(dir, "link-to-hello")); err != nil {
		t.Fatal(err)
	}
}

// engineBackup runs the full engine pipeline: init, backup, flush.
func engineBackup(t *testing.T, ctx context.Context, repoDir, sourceDir string) (string, error) {
	t.Helper()
	local := backend.NewLocal(repoDir)
	repo, err := repository.Init(ctx, local, compatPassword)
	if err != nil {
		return "", err
	}
	engine := archiver.New(repo)
	snapID, _, err := engine.Backup(ctx, archiver.BackupSpec{
		Paths:    []string{sourceDir},
		Tags:     []string{"bqckup", "site:compat"},
		Hostname: "engine-host",
		Username: "engine-user",
	})
	return snapID.String(), err
}

func TestResticCheckSnapshotsRestore(t *testing.T) {
	if !resticVersionOK(t) {
		return
	}
	ctx := context.Background()
	repoDir := filepath.Join(t.TempDir(), "repo")
	sourceDir := t.TempDir()
	buildDataset(t, sourceDir)

	snapID, err := engineBackup(t, ctx, repoDir, sourceDir)
	if err != nil {
		t.Fatal(err)
	}

	// 1. restic check must pass on an engine-made repository
	output := runRestic(t, repoDir, "check")
	if !strings.Contains(string(output), "no errors") {
		t.Fatalf("restic check output unexpected: %s", output)
	}

	// 2. restic snapshots must list exactly our snapshot
	snapshotsJSON := runRestic(t, repoDir, "snapshots", "--json")
	var snapshots []map[string]any
	if err := json.Unmarshal(snapshotsJSON, &snapshots); err != nil {
		t.Fatalf("parse restic snapshots: %v\n%s", err, snapshotsJSON)
	}
	if len(snapshots) != 1 {
		t.Fatalf("restic lists %d snapshots, want 1", len(snapshots))
	}
	if snapshots[0]["short_id"] != snapID[:8] {
		t.Fatalf("snapshot id mismatch: restic=%v engine=%s", snapshots[0]["short_id"], snapID[:8])
	}
	paths, ok := snapshots[0]["paths"].([]any)
	if !ok || len(paths) != 1 || paths[0] != sourceDir {
		t.Fatalf("snapshot paths mismatch: %v", snapshots[0]["paths"])
	}

	// 3. restic restore must reproduce the dataset byte for byte
	// (restic restores under <target>/<basename of the source path>)
	restoreDir := t.TempDir()
	runRestic(t, repoDir, "restore", "latest", "--target", restoreDir)
	restoredTree := filepath.Join(restoreDir, filepath.Base(sourceDir))
	diff := exec.Command("diff", "-r", sourceDir, restoredTree)
	if out, err := diff.CombinedOutput(); err != nil {
		t.Fatalf("restored data differs:\n%s", out)
	}

	// 4. a second backup with a 1-byte change still passes restic check
	bigPath := filepath.Join(sourceDir, "big.bin")
	data, err := os.ReadFile(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(bigPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := engineBackup(t, ctx, repoDir, sourceDir); err != nil {
		t.Fatal(err)
	}
	runRestic(t, repoDir, "check")
	snapshotsJSON = runRestic(t, repoDir, "snapshots", "--json")
	if err := json.Unmarshal(snapshotsJSON, &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("restic lists %d snapshots after 2nd backup, want 2", len(snapshots))
	}
}

// TestEngineOpensResticMadeRepo proves migration (Q4): a repository
// created by the real restic binary must open with our engine.
func TestEngineOpensResticMadeRepo(t *testing.T) {
	if !resticVersionOK(t) {
		return
	}
	ctx := context.Background()
	repoDir := filepath.Join(t.TempDir(), "repo")
	// real restic creates the repository and a snapshot
	runRestic(t, repoDir, "init")
	sourceDir := t.TempDir()
	buildDataset(t, sourceDir)
	command := exec.Command(resticBin, "--repo", repoDir, "--no-cache", "backup", sourceDir)
	command.Env = append(os.Environ(), "RESTIC_PASSWORD="+compatPassword)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restic backup failed: %v\n%s", err, out)
	}

	// our engine opens it and continues: a new backup dedups against the
	// existing index
	local := backend.NewLocal(repoDir)
	repo, err := repository.Open(ctx, local, compatPassword)
	if err != nil {
		t.Fatalf("engine could not open restic-made repo: %v", err)
	}
	if _, _, err := archiver.New(repo).Backup(ctx, archiver.BackupSpec{Paths: []string{sourceDir}}); err != nil {
		t.Fatalf("engine backup into restic-made repo failed: %v", err)
	}
	// and restic still accepts the result
	runRestic(t, repoDir, "check")
}
