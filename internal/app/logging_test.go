package app

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAppLoggerWritesConfiguredFileWithProtectedMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bqckup.log")
	logger, closeLogger, err := openAppLogger(config.App{LogFile: path, LogLevel: "info"})
	require.NoError(t, err)
	logger.write(logDebug, "event=debug_should_be_filtered")
	logger.write(logInfo, "event=backup_start site=example")
	require.NoError(t, closeLogger())

	info, err := os.Stat(path)
	require.NoError(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log file mode = %o, want 600", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(contents)
	require.Contains(t, text, "event=backup_start site=example")
	if strings.Contains(text, "debug_should_be_filtered") {
		t.Fatal("debug event was written below the configured info level")
	}
}

func TestLoggingProgressWritesDetailedStageLifecycle(t *testing.T) {
	var output bytes.Buffer
	logger := &appLogger{logger: log.New(&output, "", 0), level: logInfo}
	progress := newLoggingProgress(logger, "example", nil)

	progress.StartStage("upload primary", 10)
	progress.Add(7)
	progress.FinishStage()

	text := output.String()
	require.Contains(t, text, `event=stage_start site="example" stage="upload primary" total_bytes=10`)
	require.Contains(t, text, `event=stage_finished site="example" stage="upload primary" status="success" completed_bytes=7 total_bytes=10 duration_ms=`)
	require.Equal(t, 2, strings.Count(text, "\n"))
}
