package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	backupincremental "github.com/bqckup/bqckup-go/internal/backup/incremental"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func healthyOutcome() backup.CheckOutcome {
	return backup.CheckOutcome{Site: "site-a", Destination: "s3-primary", Mode: "incremental", Result: backupincremental.CheckResult{
		Status: "healthy", DurationSeconds: 1.23, Indexes: 12, Snapshots: 45, Packs: 300, Blobs: 12000,
	}}
}

func findingsOutcome(findings ...backupincremental.Finding) backup.CheckOutcome {
	outcome := healthyOutcome()
	outcome.Result.Status = "problems"
	outcome.Result.Findings = findings
	return outcome
}

func TestCheckTextHealthy(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, writeCheckText(&out, healthyOutcome()))
	assert.Equal(t, "check site-a/s3-primary: healthy\n", out.String())
}

func TestCheckTextProblemsMatchesPlanSample(t *testing.T) {
	hex := func(c byte, n int) string { return strings.Repeat(string(c), n) }
	findings := []backupincremental.Finding{
		{Type: "broken_index", ID: hex('a', 64)},
		{Type: "broken_index", ID: hex('b', 64)},
		{Type: "missing_pack", ID: hex('c', 64), BlobCount: 3},
		{Type: "orphaned_pack", ID: hex('d', 64)},
		{Type: "orphaned_pack", ID: hex('e', 64)},
		{Type: "orphaned_pack", ID: hex('f', 64)},
	}
	var out bytes.Buffer
	require.NoError(t, writeCheckText(&out, findingsOutcome(findings...)))
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	assert.Equal(t, "check site-a/s3-primary: problems found (2 broken_index, 1 missing_pack, 3 orphaned_pack)", lines[0])
	assert.Equal(t, "broken_index "+hex('a', 64), lines[1])
	assert.Equal(t, "broken_index "+hex('b', 64), lines[2])
	assert.Equal(t, "missing_pack "+hex('c', 64)+" (3 blobs)", lines[3])
	assert.Equal(t, "orphaned_pack "+hex('d', 64), lines[4])
}

func TestCheckFindingLines(t *testing.T) {
	hex := func(c byte, n int) string { return strings.Repeat(string(c), n) }
	assert.Equal(t, "broken_config config", checkFindingLine(backupincremental.Finding{Type: "broken_config"}))
	assert.Equal(t, "broken_key "+hex('1', 64), checkFindingLine(backupincremental.Finding{Type: "broken_key", ID: hex('1', 64)}))
	assert.Equal(t, "broken_snapshot "+hex('2', 64), checkFindingLine(backupincremental.Finding{Type: "broken_snapshot", ID: hex('2', 64)}))
	assert.Equal(t, "broken_pack "+hex('3', 64), checkFindingLine(backupincremental.Finding{Type: "broken_pack", ID: hex('3', 64)}))
	assert.Equal(t, "missing_blob "+hex('4', 64)+" (snapshot "+hex('5', 64)+")",
		checkFindingLine(backupincremental.Finding{Type: "missing_blob", ID: hex('4', 64), SnapshotID: hex('5', 64)}))
	assert.Equal(t, "corrupt_blob "+hex('6', 64)+" (snapshot "+hex('7', 64)+")",
		checkFindingLine(backupincremental.Finding{Type: "corrupt_blob", ID: hex('6', 64), SnapshotID: hex('7', 64)}))
	assert.Equal(t, "corrupt_blob "+hex('6', 64)+" (pack "+hex('8', 64)+")",
		checkFindingLine(backupincremental.Finding{Type: "corrupt_blob", ID: hex('6', 64), PackID: hex('8', 64)}))
	assert.Equal(t, "missing_pack "+hex('9', 64)+" (7 blobs)",
		checkFindingLine(backupincremental.Finding{Type: "missing_pack", ID: hex('9', 64), BlobCount: 7}))
}

func TestCheckTextCapsAtHundredFindings(t *testing.T) {
	findings := make([]backupincremental.Finding, 0, 105)
	for i := 0; i < 105; i++ {
		findings = append(findings, backupincremental.Finding{Type: "orphaned_pack", ID: fmt.Sprintf("%064x", i)})
	}
	var out bytes.Buffer
	require.NoError(t, writeCheckText(&out, findingsOutcome(findings...)))
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	assert.Equal(t, 102, len(lines), "summary + 100 findings + remainder note")
	assert.Equal(t, "and 5 more findings (see --findings-file)", lines[len(lines)-1])
}

func TestCheckJSONSchema(t *testing.T) {
	findings := []backupincremental.Finding{
		{Type: "missing_pack", ID: strings.Repeat("a", 64), BlobCount: 3},
	}
	outcome := findingsOutcome(findings...)
	outcome.Result.ReadData = true
	root, stdout, _ := commandForTest(t, "version")
	require.NoError(t, writeCheckJSON(root, outcome))
	var got map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, "site-a", got["site"])
	assert.Equal(t, "s3-primary", got["destination"])
	assert.Equal(t, "incremental", got["mode"])
	assert.Equal(t, true, got["read_data"])
	assert.Equal(t, "problems", got["status"])
	assert.Equal(t, float64(1.23), got["duration_seconds"])
	assert.Equal(t, float64(12), got["indexes"])
	assert.Equal(t, float64(45), got["snapshots"])
	assert.Equal(t, float64(300), got["packs"])
	assert.Equal(t, float64(12000), got["blobs"])
	findingsJSON := got["findings"].([]any)
	require.Len(t, findingsJSON, 1)
	entry := findingsJSON[0].(map[string]any)
	assert.Equal(t, "missing_pack", entry["type"])
	assert.Equal(t, strings.Repeat("a", 64), entry["id"])
	assert.Equal(t, float64(3), entry["blob_count"])
}

func TestCheckFindingsFileTextAndJSON(t *testing.T) {
	findings := []backupincremental.Finding{
		{Type: "broken_index", ID: strings.Repeat("b", 64)},
		{Type: "orphaned_pack", ID: strings.Repeat("c", 64)},
	}
	dir := t.TempDir()
	textPath := filepath.Join(dir, "findings.txt")
	require.NoError(t, writeFindingsFile(textPath, "text", findings))
	raw, err := os.ReadFile(textPath)
	require.NoError(t, err)
	assert.Equal(t, "broken_index "+strings.Repeat("b", 64)+"\norphaned_pack "+strings.Repeat("c", 64)+"\n", string(raw))

	jsonPath := filepath.Join(dir, "findings.json")
	require.NoError(t, writeFindingsFile(jsonPath, "json", findings))
	raw, err = os.ReadFile(jsonPath)
	require.NoError(t, err)
	var decoded []map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Len(t, decoded, 2)
	assert.Equal(t, "orphaned_pack", decoded[1]["type"])
}

func TestCheckFindingsFileWrittenEvenWithZeroFindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.txt")
	require.NoError(t, writeFindingsFile(path, "text", nil))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Empty(t, raw)
	jsonPath := filepath.Join(t.TempDir(), "findings.json")
	require.NoError(t, writeFindingsFile(jsonPath, "json", nil))
	raw, err = os.ReadFile(jsonPath)
	require.NoError(t, err)
	assert.Equal(t, "[]\n", string(raw))
}

func TestCheckFindingsFileWriteFailureIsPreflight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "findings.txt")
	err := writeFindingsFile(path, "text", nil)
	require.Error(t, err)
	assert.Equal(t, apperror.CategoryPreflight, apperror.CategoryOf(apperror.Wrap(apperror.CategoryPreflight, "could not write the findings file", err)))
}

func TestExitCodeCheckProblems(t *testing.T) {
	assert.Equal(t, 1, ExitCode(errCheckProblems))
	assert.Equal(t, 0, ExitCode(nil))
}

func TestBackupCheckRequiresDestination(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "check", "example")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestBackupCheckRequiresSite(t *testing.T) {
	root, _, _ := commandForTest(t, "backup", "check")
	err := root.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidInput)
	assert.Equal(t, 2, ExitCode(err))
}

func TestBackupCheckFullModeSiteFails(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "check", "example", "--destination", "local-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
	message := apperror.UserMessage(err)
	assert.Contains(t, message, "history list")
	assert.Contains(t, message, "--details")
}

func TestBackupCheckMissingPasswordFails(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	writeIncrementalSiteConfig(t, configDir, "")
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "check", "site-b", "--destination", "local-primary")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
}

// checkSeedConfigDir builds an incremental site backed by a real source
// directory on the writeCLIConfig local storage.
func checkSeedConfigDir(t *testing.T) (configDir, backupRoot string) {
	t.Helper()
	configDir, backupRoot = writeCLIConfig(t)
	source := filepath.Join(filepath.Dir(configDir), "check-source")
	require.NoError(t, os.MkdirAll(source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "f.txt"), []byte("check payload"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "sites", "site-b.yaml"), []byte(fmt.Sprintf(`version: 2
site:
  name: site-b
  enabled: true
  backup_mode: incremental
  incremental:
    password: "supersecret"
  sources:
    files:
      include: [%s]
      exclude: []
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1h
`, source)), 0o600))
	return configDir, backupRoot
}

func runCheckCommand(t *testing.T, configDir string, args ...string) (int, string) {
	t.Helper()
	full := append([]string{"--config-dir", configDir}, args...)
	root, stdout, _ := commandForTest(t, full...)
	err := root.Execute()
	return ExitCode(err), stdout.String()
}

func TestBackupCheckHealthyRepositoryExitsZero(t *testing.T) {
	configDir, _ := checkSeedConfigDir(t)
	if code, _ := runCheckCommand(t, configDir, "backup", "run", "site-b", "--force"); code != 0 {
		t.Fatalf("seed backup failed with exit %d", code)
	}
	code, stdout := runCheckCommand(t, configDir, "backup", "check", "site-b", "--destination", "local-primary")
	assert.Equal(t, 0, code)
	assert.Equal(t, "check site-b/local-primary: healthy\n", stdout)
}

func TestBackupCheckCorruptRepositoryExitsOneWithFindings(t *testing.T) {
	configDir, backupRoot := checkSeedConfigDir(t)
	if code, _ := runCheckCommand(t, configDir, "backup", "run", "site-b", "--force"); code != 0 {
		t.Fatalf("seed backup failed with exit %d", code)
	}
	// remove one stored pack: the index still references it
	repoDir := filepath.Join(backupRoot, "restic", "site-b")
	var removed bool
	err := filepath.WalkDir(filepath.Join(repoDir, "data"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || removed || entry.IsDir() {
			return err
		}
		removed = true
		return os.Remove(path)
	})
	require.NoError(t, err)
	require.True(t, removed, "no pack file found to corrupt")

	code, stdout := runCheckCommand(t, configDir, "backup", "check", "site-b", "--destination", "local-primary")
	assert.Equal(t, 1, code)
	assert.Contains(t, stdout, "check site-b/local-primary: problems found")
	assert.Contains(t, stdout, "missing_pack ")
}

func TestBackupCheckJSONOutputAndFindingsFile(t *testing.T) {
	configDir, _ := checkSeedConfigDir(t)
	if code, _ := runCheckCommand(t, configDir, "backup", "run", "site-b", "--force"); code != 0 {
		t.Fatalf("seed backup failed with exit %d", code)
	}
	findingsPath := filepath.Join(t.TempDir(), "findings.txt")
	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "check", "site-b", "--destination", "local-primary", "--findings-file", findingsPath)
	require.NoError(t, root.Execute())
	var report map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	assert.Equal(t, "site-b", report["site"])
	assert.Equal(t, "local-primary", report["destination"])
	assert.Equal(t, "incremental", report["mode"])
	assert.Equal(t, false, report["read_data"])
	assert.Equal(t, "healthy", report["status"])
	raw, err := os.ReadFile(findingsPath)
	require.NoError(t, err)
	assert.Equal(t, "[]\n", string(raw))
}

func TestBackupCheckReadDataFlagReachesReport(t *testing.T) {
	configDir, _ := checkSeedConfigDir(t)
	if code, _ := runCheckCommand(t, configDir, "backup", "run", "site-b", "--force"); code != 0 {
		t.Fatalf("seed backup failed with exit %d", code)
	}
	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "check", "site-b", "--destination", "local-primary", "--read-data")
	require.NoError(t, root.Execute())
	var report map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	assert.Equal(t, true, report["read_data"])
	assert.Equal(t, "healthy", report["status"])
}

func TestBackupCheckNoRepositoryIsCommandFailure(t *testing.T) {
	configDir, _ := checkSeedConfigDir(t)
	code, stdout := runCheckCommand(t, configDir, "backup", "check", "site-b", "--destination", "local-primary")
	assert.Equal(t, 4, code)
	assert.NotContains(t, stdout, "supersecret")
	assert.NotContains(t, stdout, "problems found")
}

func TestBackupCheckReportsTextProgress(t *testing.T) {
	configDir, _ := checkSeedConfigDir(t)
	if code, _ := runCheckCommand(t, configDir, "backup", "run", "site-b", "--force"); code != 0 {
		t.Fatalf("seed backup failed with exit %d", code)
	}
	root, stdout, stderr := commandForTest(t, "--config-dir", configDir, "backup", "check", "site-b", "--destination", "local-primary")
	require.NoError(t, root.Execute())
	assert.Equal(t, "[>] check:site-b: checking repository on local-primary\n", stderr.String())
	assert.Equal(t, "check site-b/local-primary: healthy\n", stdout.String())

	// Test with --read-data
	rootRD, stdoutRD, stderrRD := commandForTest(t, "--config-dir", configDir, "backup", "check", "site-b", "--destination", "local-primary", "--read-data")
	require.NoError(t, rootRD.Execute())
	assert.Equal(t, "[>] check:site-b: checking repository on local-primary (read-data)\n", stderrRD.String())
	assert.Equal(t, "check site-b/local-primary: healthy\n", stdoutRD.String())
}

func TestBackupCheckJSONModeSuppressesStderrProgress(t *testing.T) {
	configDir, _ := checkSeedConfigDir(t)
	if code, _ := runCheckCommand(t, configDir, "backup", "run", "site-b", "--force"); code != 0 {
		t.Fatalf("seed backup failed with exit %d", code)
	}
	root, stdout, stderr := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "check", "site-b", "--destination", "local-primary")
	require.NoError(t, root.Execute())
	assert.Empty(t, stderr.String())
	assert.NotEmpty(t, stdout.String())
}

