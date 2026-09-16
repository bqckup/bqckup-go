package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
)

type appLogger struct {
	logger *slog.Logger
}

const (
	logDebug = slog.LevelDebug
	logInfo  = slog.LevelInfo
	logWarn  = slog.LevelWarn
	logError = slog.LevelError
)

func openAppLogger(appConfig config.App) (*appLogger, func() error, error) {
	if appConfig.LogFile == "" {
		return newAppLogger(io.Discard, logInfo), func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(appConfig.LogFile), 0o750); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(appConfig.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return newAppLogger(file, logLevelValue(appConfig.LogLevel)), file.Close, nil
}

func newAppLogger(writer io.Writer, level slog.Level) *appLogger {
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level})
	return &appLogger{logger: slog.New(handler)}
}

func logLevelValue(level string) slog.Level {
	switch level {
	case "debug":
		return logDebug
	case "warn":
		return logWarn
	case "error":
		return logError
	default:
		return logInfo
	}
}

func (l *appLogger) write(level slog.Level, event string, args ...any) {
	if l != nil {
		l.logger.Log(context.Background(), level, event, append([]any{"event", event}, args...)...)
	}
}

type loggingProgress struct {
	logger      *appLogger
	site        string
	next        backup.Progress
	stage       string
	total       int64
	completed   int64
	stageStart  time.Time
	stageActive bool
}

func newLoggingProgress(logger *appLogger, site string, next backup.Progress) *loggingProgress {
	if next == nil {
		next = backup.NoopProgress{}
	}
	return &loggingProgress{logger: logger, site: site, next: next}
}

func (p *loggingProgress) StartStage(label string, total int64) {
	p.stage = label
	p.total = total
	p.completed = 0
	p.stageStart = time.Now()
	p.stageActive = true
	p.logger.write(logInfo, "stage_start", "site", p.site, "stage", label, "total_bytes", total)
	p.next.StartStage(label, total)
}

func (p *loggingProgress) Add(units int64) {
	p.completed += units
	p.next.Add(units)
}

func (p *loggingProgress) FinishStage() {
	p.finish("success", logInfo)
	p.next.FinishStage()
}

func (p *loggingProgress) FailStage() {
	p.finish("failed", logError)
	p.next.FailStage()
}

func (p *loggingProgress) Done() {
	p.next.Done()
}

func (p *loggingProgress) finish(status string, level slog.Level) {
	if !p.stageActive {
		return
	}
	p.logger.write(level, "stage_finished",
		"site", p.site,
		"stage", p.stage,
		"status", status,
		"completed_bytes", p.completed,
		"total_bytes", p.total,
		"duration_ms", time.Since(p.stageStart).Milliseconds(),
	)
	p.stageActive = false
}
