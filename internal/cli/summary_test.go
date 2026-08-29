package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/history"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func summaryTestConfig() config.Config {
	return config.Config{
		Storages: map[string]config.Storage{
			"s3-primary": {Type: "s3", Primary: true},
			"home":       {Type: "local"},
		},
		Sites: []config.Site{
			{Name: "web", Enabled: true, BackupMode: "full", Destinations: []config.Destination{{Storage: "s3-primary"}, {Storage: "home"}}, Policy: config.Policy{KeepLast: 7}},
			{Name: "db", Enabled: false, BackupMode: "full", Destinations: []config.Destination{{Storage: "home"}}, Policy: config.Policy{KeepLast: 3}},
		},
	}
}

func summaryRun(site string, status history.RunStatus, started time.Time, packages ...history.Package) history.BackupRun {
	run := history.BackupRun{
		ID: site + "-" + string(status), SiteName: site, Status: status,
		StartedAt: started, DurationMillis: 2000, Packages: packages,
	}
	if status != history.StatusRunning {
		finished := started.Add(2 * time.Second)
		run.FinishedAt = &finished
	}
	return run
}

func TestBuildSummariesStatusSemantics(t *testing.T) {
	now := time.Now().UTC()
	runs := []history.BackupRun{
		summaryRun("web", history.StatusRunning, now),
		summaryRun("db", history.StatusSuccess, now.Add(-time.Hour)),
	}
	views := buildSummaries(summaryTestConfig(), runs, "")
	require.Len(t, views, 2)
	assert.Equal(t, "db", views[0].Name) // sorted by name
	assert.Equal(t, "disabled", views[0].Status)
	assert.Equal(t, "running", views[1].Status)

	runs[0].Status = history.StatusSuccess
	views = buildSummaries(summaryTestConfig(), runs, "")
	assert.Equal(t, "idle", views[1].Status)
}

func TestBuildSummariesLogicalDedupAcrossDestinations(t *testing.T) {
	run := summaryRun("web", history.StatusSuccess, time.Now().UTC(),
		history.Package{SourceKind: "file", SourceName: "data.txt", Destination: "s3-primary", Size: 100},
		history.Package{SourceKind: "file", SourceName: "data.txt", Destination: "home", Size: 40},
		history.Package{SourceKind: "database", SourceName: "app", Destination: "s3-primary", Size: 10},
	)
	views := buildSummaries(summaryTestConfig(), []history.BackupRun{run}, "")
	view := views[1]
	assert.Equal(t, "web", view.Name)
	assert.Equal(t, 1, view.SuccessfulBackups)
	assert.Equal(t, int64(110), view.TotalRecordedSize) // max(100, 40) + 10
	require.NotNil(t, view.LastBackupSize)
	assert.Equal(t, int64(110), *view.LastBackupSize)
}

func TestBuildSummariesIgnoresOrphanRuns(t *testing.T) {
	runs := []history.BackupRun{
		summaryRun("ghost", history.StatusSuccess, time.Now().UTC(),
			history.Package{SourceKind: "file", SourceName: "x", Destination: "home", Size: 999}),
	}
	for _, view := range buildSummaries(summaryTestConfig(), runs, "") {
		assert.Equal(t, 0, view.SuccessfulBackups)
		assert.Equal(t, int64(0), view.TotalRecordedSize)
		assert.Nil(t, view.LastBackupAt)
	}
}

func TestBuildSummariesNeverRunAndFailedLastRun(t *testing.T) {
	cfg := summaryTestConfig()
	for _, view := range buildSummaries(cfg, nil, "") {
		assert.Nil(t, view.LastBackupAt)
		assert.Nil(t, view.LastBackupStatus)
		assert.Nil(t, view.LastBackupDurationMillis)
		assert.Nil(t, view.LastBackupSize)
	}

	failed := summaryRun("web", history.StatusFailed, time.Now().UTC())
	view := buildSummaries(cfg, []history.BackupRun{failed}, "web")[0]
	assert.Equal(t, "idle", view.Status)
	require.NotNil(t, view.LastBackupStatus)
	assert.Equal(t, "failed", *view.LastBackupStatus)
	assert.Nil(t, view.LastBackupSize) // size only on success
	require.NotNil(t, view.LastBackupDurationMillis)
	assert.Equal(t, int64(2000), *view.LastBackupDurationMillis)
}

func TestBuildSummariesIncrementalSizesAsRecorded(t *testing.T) {
	run := summaryRun("web", history.StatusSuccess, time.Now().UTC(),
		history.Package{SourceKind: "snapshot", SourceName: "web", Destination: "s3-primary", Size: 500},
		history.Package{SourceKind: "snapshot", SourceName: "web", Destination: "home", Size: 300},
	)
	view := buildSummaries(summaryTestConfig(), []history.BackupRun{run}, "web")[0]
	assert.Equal(t, int64(500), view.TotalRecordedSize) // largest copy wins
}

func TestBuildSummariesFilter(t *testing.T) {
	views := buildSummaries(summaryTestConfig(), nil, "web")
	require.Len(t, views, 1)
	assert.Equal(t, "web", views[0].Name)
}

func TestWriteSummaryTextPanels(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	defer func() { time.Local = previous }()

	started := time.Date(2026, 8, 22, 17, 46, 0, 0, time.UTC)
	run := summaryRun("web", history.StatusSuccess, started,
		history.Package{SourceKind: "file", SourceName: "data.txt", Destination: "s3-primary", Size: 1024},
	)
	views := buildSummaries(summaryTestConfig(), []history.BackupRun{run}, "")
	var output bytes.Buffer
	require.NoError(t, writeSummaryText(&output, views))
	text := output.String()

	assert.True(t, strings.Index(text, "Backup Summary for db") < strings.Index(text, "Backup Summary for web"), "sites sorted by name")
	assert.NotContains(t, text, "\x1b[") // colors only on a TTY
	assert.Contains(t, text, "Status              : disabled\n")
	assert.Contains(t, text, "Enabled             : no\n")
	assert.Contains(t, text, "Status              : idle\n")
	assert.Contains(t, text, "Enabled             : yes\n")
	assert.Contains(t, text, "Backup Mode         : full\n")
	assert.Contains(t, text, "Last Backup         : 22 Aug 2026, 17:46 UTC\n")
	assert.Contains(t, text, "Last Backup Status  : success\n")
	assert.Contains(t, text, "Last Backup Duration: 2s\n")
	assert.Contains(t, text, "Last Backup Size    : 1.0 KiB\n")
	assert.Contains(t, text, "Successful Backups  : 1\n")
	assert.Contains(t, text, "Total Recorded Size : 1.0 KiB\n")
	assert.Contains(t, text, "Destinations        : s3-primary (s3) [primary], home (local)\n")
	assert.Contains(t, text, "Retention           : keep last 7\n")

	// Never-run site renders dashes and zero counters.
	var fresh bytes.Buffer
	views = buildSummaries(summaryTestConfig(), nil, "db")
	require.NoError(t, writeSummaryText(&fresh, views))
	assert.Contains(t, fresh.String(), "Last Backup         : -\n")
	assert.Contains(t, fresh.String(), "Last Backup Status  : -\n")
	assert.Contains(t, fresh.String(), "Last Backup Duration: -\n")
	assert.Contains(t, fresh.String(), "Last Backup Size    : -\n")
	assert.Contains(t, fresh.String(), "Successful Backups  : 0\n")
	assert.Contains(t, fresh.String(), "Total Recorded Size : 0 B\n")
	assert.Contains(t, fresh.String(), "Retention           : keep last 3\n")
}

func TestWriteSummaryTextRunningShowsInProgress(t *testing.T) {
	views := buildSummaries(summaryTestConfig(),
		[]history.BackupRun{summaryRun("web", history.StatusRunning, time.Now().UTC())}, "web")
	var output bytes.Buffer
	require.NoError(t, writeSummaryText(&output, views))
	assert.Contains(t, output.String(), "Last Backup Duration: in progress\n")
}

func TestWriteSummaryTextEmptyConfig(t *testing.T) {
	var output bytes.Buffer
	require.NoError(t, writeSummaryText(&output, nil))
	assert.Equal(t, "No backup sites configured.\n", output.String())
}

func TestBackupSummaryCommandText(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "run", "example", "--force")
	require.NoError(t, root.Execute())

	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "backup", "summary")
	require.NoError(t, root.Execute())
	text := stdout.String()
	assert.NotContains(t, text, "\x1b[") // colors only on a TTY
	assert.Contains(t, text, "Status              : idle\n")
	assert.Contains(t, text, "Enabled             : yes\n")
	assert.Contains(t, text, "Successful Backups  : 1\n")
	assert.Contains(t, text, "Last Backup Status  : success\n")
	assert.Contains(t, text, "Last Backup Size    : ")
	assert.Contains(t, text, "Destinations        : local-primary (local)\n")
	assert.Contains(t, text, "Retention           : keep last 3\n")
}

func TestBackupSummaryCommandDisabledAndNeverRun(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	writeCLISite(t, configDir, "fresh", true)
	writeCLISite(t, configDir, "site-disabled", false)

	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "backup", "summary")
	require.NoError(t, root.Execute())
	text := stdout.String()
	assert.Contains(t, text, "Backup Summary for fresh\n")
	assert.Contains(t, text, "Last Backup         : -\n")
	assert.Contains(t, text, "Successful Backups  : 0\n")
	assert.Contains(t, text, "Backup Summary for site-disabled\n")
	assert.Contains(t, text, "Status              : disabled\n")
	assert.Contains(t, text, "Enabled             : no\n")
}

func TestBackupSummaryCommandJSON(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	writeCLISite(t, configDir, "fresh", true)
	root, _, _ := commandForTest(t, "--config-dir", configDir, "backup", "run", "example", "--force")
	require.NoError(t, root.Execute())

	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "summary")
	require.NoError(t, root.Execute())
	var views []summaryView
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &views))
	require.Len(t, views, 2)
	assert.Equal(t, "example", views[0].Name)
	assert.Equal(t, "fresh", views[1].Name) // sorted by name
	view := views[0]
	assert.Equal(t, "idle", view.Status)
	require.NotNil(t, views[0].LastBackupStatus)
	assert.Equal(t, "success", *views[0].LastBackupStatus)
	require.NotNil(t, views[0].LastBackupSize)
	require.NotNil(t, views[0].Destinations)
	assert.Equal(t, "local", views[0].Destinations[0].Type)

	root, stdout, _ = commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "summary", "--site", "example")
	require.NoError(t, root.Execute())
	var single summaryView
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &single))
	assert.Equal(t, "example", single.Name)

	root, stdout, _ = commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "summary", "--site", "fresh")
	require.NoError(t, root.Execute())
	var fresh summaryView
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &fresh))
	assert.Nil(t, fresh.LastBackupAt)
	assert.Nil(t, fresh.LastBackupStatus)

	root, _, _ = commandForTest(t, "--config-dir", configDir, "backup", "summary", "--site", "nope")
	err := root.Execute()
	require.Error(t, err)
	assert.Equal(t, 2, ExitCode(err))
}

func TestBackupSummaryCommandEmptyConfig(t *testing.T) {
	configDir, _ := writeCLIConfig(t)
	require.NoError(t, os.Remove(filepath.Join(configDir, "sites", "example.yaml")))

	root, stdout, _ := commandForTest(t, "--config-dir", configDir, "backup", "summary")
	require.NoError(t, root.Execute())
	assert.Equal(t, "No backup sites configured.\n", stdout.String())

	root, stdout, _ = commandForTest(t, "--config-dir", configDir, "--output", "json", "backup", "summary")
	require.NoError(t, root.Execute())
	assert.Equal(t, "[]", strings.TrimSpace(stdout.String()))
}

func TestAnsiColorWrap(t *testing.T) {
	assert.Equal(t, "idle", ansiColor{}.status("idle"))
	on := ansiColor{on: true}
	assert.Equal(t, "\x1b[1mBackup Summary for web\x1b[0m", on.bold("Backup Summary for web"))
	assert.Equal(t, "\x1b[2mdisabled\x1b[0m", on.status("disabled"))
	assert.Equal(t, "\x1b[33mrunning\x1b[0m", on.status("running"))
	assert.Equal(t, "\x1b[32midle\x1b[0m", on.status("idle"))
}
