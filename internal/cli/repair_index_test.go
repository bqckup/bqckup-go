package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bqckup/bqckup-go/internal/backup"
	backuprestic "github.com/bqckup/bqckup-go/internal/backup/restic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func repairOutcome() backup.RepairOutcome {
	return backup.RepairOutcome{
		Site:        "site-a",
		Destination: "s3-primary",
		Mode:        "incremental",
		Result: backuprestic.RepairResult{
			DurationSeconds:   1.23,
			PacksProcessed:    5,
			BlobsIndexed:      42,
			OldIndexesRemoved: 2,
			NewIndexesWritten: 1,
		},
	}
}

func TestRepairIndexTextOutput(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, writeRepairText(&out, repairOutcome()))
	assert.Equal(t, "repair-index site-a/s3-primary: 5 packs processed, 42 blobs indexed, 2 old indexes removed, 1 new index written\n", out.String())
}

func TestRepairIndexJSONOutput(t *testing.T) {
	outcome := repairOutcome()
	root, stdout, _ := commandForTest(t, "version")
	require.NoError(t, writeRepairJSON(root, outcome))
	var got map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, "site-a", got["site"])
	assert.Equal(t, "s3-primary", got["destination"])
	assert.Equal(t, "incremental", got["mode"])
	assert.Equal(t, "repaired", got["status"])
	assert.Equal(t, float64(1.23), got["duration_seconds"])
	assert.Equal(t, float64(5), got["packs_processed"])
	assert.Equal(t, float64(42), got["blobs_indexed"])
	assert.Equal(t, float64(2), got["old_indexes_removed"])
	assert.Equal(t, float64(1), got["new_indexes_written"])
}

func TestRepairIndexRequiresSiteArgument(t *testing.T) {
	root, _, _ := commandForTest(t, "backup", "repair-index")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestRepairIndexRequiresDestinationFlag(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "repair-index", "example")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestRepairIndexExecutionError(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "supersecret")
	configDir, _ := checkSeedConfigDir(t)
	// Without running backup first, no repository exists on destination -> storage error (exit code 4)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "repair-index", "site-b", "--destination", "local-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 4, ExitCode(err))
}

func TestRepairIndexFullModeSiteFails(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "repair-index", "example", "--destination", "local-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
}

func TestRepairIndexEndToEndCommand(t *testing.T) {
	t.Setenv("RESTIC_PASSWORD", "supersecret")
	configDir, backupRoot := checkSeedConfigDir(t)
	if code, _ := runCheckCommand(t, configDir, "backup", "run", "site-b", "--force"); code != 0 {
		t.Fatalf("seed backup failed with exit %d", code)
	}

	// Corrupt index by deleting it from local storage
	repoDir := filepath.Join(backupRoot, "restic", "site-b")
	indexDir := filepath.Join(repoDir, "index")
	entries, err := os.ReadDir(indexDir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NoError(t, os.Remove(filepath.Join(indexDir, entry.Name())))
	}

	// Now check reports problems
	code, stdout := runCheckCommand(t, configDir, "backup", "check", "site-b", "--destination", "local-primary")
	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "problems found")

	// Run repair-index command in text mode
	root, stdoutBuf, _ := commandForTest(t, "--config-dir", configDir, "backup", "repair-index", "site-b", "--destination", "local-primary")
	require.NoError(t, root.Execute())
	assert.Contains(t, stdoutBuf.String(), "repair-index site-b/local-primary:")
	assert.Contains(t, stdoutBuf.String(), "packs processed")
	assert.Contains(t, stdoutBuf.String(), "1 new index written")

	// Now check passes again (healthy)
	code, stdout = runCheckCommand(t, configDir, "backup", "check", "site-b", "--destination", "local-primary")
	assert.Equal(t, 0, code)
	assert.Contains(t, stdout, "healthy")

	// Run repair-index command in JSON mode
	rootJSON, stdoutJSON, _ := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "repair-index", "site-b", "--destination", "local-primary")
	require.NoError(t, rootJSON.Execute())
	var report map[string]any
	require.NoError(t, json.Unmarshal(stdoutJSON.Bytes(), &report))
	assert.Equal(t, "site-b", report["site"])
	assert.Equal(t, "local-primary", report["destination"])
	assert.Equal(t, "incremental", report["mode"])
	assert.Equal(t, "repaired", report["status"])
	assert.Greater(t, report["packs_processed"], float64(0))
	assert.Greater(t, report["blobs_indexed"], float64(0))
	assert.Equal(t, float64(1), report["new_indexes_written"])
}
