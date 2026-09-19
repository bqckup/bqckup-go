package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAppLoggerWritesConfiguredFileWithProtectedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bqckup.log")
	logger, closeLogger, err := openAppLogger(config.App{LogFile: path, LogLevel: "info"})
	require.NoError(t, err)
	logger.write(logDebug, "debug_should_be_filtered")
	logger.write(logInfo, "backup_start", "site", "example")
	require.NoError(t, closeLogger())

	info, err := os.Stat(path)
	require.NoError(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log file mode = %o, want 600", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(contents)
	require.Contains(t, text, `"event":"backup_start"`)
	if strings.Contains(text, "debug_should_be_filtered") {
		t.Fatal("debug event was written below the configured info level")
	}
	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(contents), &event))
	require.Equal(t, "INFO", event["level"])
	require.Equal(t, "backup_start", event["event"])
	require.Equal(t, "example", event["site"])
}

func TestLoggingProgressWritesDetailedStageLifecycle(t *testing.T) {
	var output bytes.Buffer
	logger := newAppLogger(&output, logInfo)
	progress := newLoggingProgress(logger, "example", nil)

	progress.StartStage("upload primary", 10)
	progress.Add(7)
	progress.FinishStage()

	text := output.String()
	require.Contains(t, text, `"event":"stage_start","site":"example","stage":"upload primary","total_bytes":10`)
	require.Contains(t, text, `"event":"stage_finished","site":"example","stage":"upload primary","status":"success","completed_bytes":7,"total_bytes":10,"duration_ms":`)
	require.Equal(t, 2, strings.Count(text, "\n"))
}

func TestAppLoggerWritesDebugAtConfiguredLevel(t *testing.T) {
	var output bytes.Buffer
	logger := newAppLogger(&output, logDebug)

	logger.write(logDebug, "backup_plan_detail", "site", "example")

	var event map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event))
	require.Equal(t, "DEBUG", event["level"])
	require.Equal(t, "backup_plan_detail", event["event"])
}

func TestLogBackupFinishedRecordsFailureDetails(t *testing.T) {
	var output bytes.Buffer
	application := &App{logger: newAppLogger(&output, logInfo)}
	err := apperror.Wrap(apperror.CategoryExecution, "could not export database", errors.New("mysqldump failed"))

	application.logBackupFinished("example", backup.RunResult{
		RunID:  "run-1",
		Status: backup.StatusFailed,
	}, time.Now().Add(-time.Second), err)

	text := output.String()
	require.Contains(t, text, `"event":"backup_finished"`)
	require.Contains(t, text, `"status":"failed"`)
	require.Contains(t, text, `"category":"execution"`)
	require.Contains(t, text, `"error":"could not export database: mysqldump failed"`)
}
