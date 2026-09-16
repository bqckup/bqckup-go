package app

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
)

type appLogger struct {
	logger *log.Logger
	level  int
}

const (
	logDebug = iota
	logInfo
	logWarn
	logError
)

func openAppLogger(appConfig config.App) (*appLogger, func() error, error) {
	if appConfig.LogFile == "" {
		return &appLogger{logger: log.New(io.Discard, "", 0), level: logInfo}, func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(appConfig.LogFile), 0o750); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(appConfig.LogFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return &appLogger{logger: log.New(file, "", log.Ldate|log.Ltime|log.LUTC), level: logLevelValue(appConfig.LogLevel)}, file.Close, nil
}

func logLevelValue(level string) int {
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

func (l *appLogger) write(level int, message string) {
	if l != nil && level >= l.level {
		l.logger.Printf("level=%s %s", logLevelName(level), message)
	}
}

func logLevelName(level int) string {
	switch level {
	case logDebug:
		return "debug"
	case logWarn:
		return "warn"
	case logError:
		return "error"
	default:
		return "info"
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
	p.logger.write(logInfo, fmt.Sprintf("event=stage_start site=%q stage=%q total_bytes=%d", p.site, label, total))
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

func (p *loggingProgress) finish(status string, level int) {
	if !p.stageActive {
		return
	}
	p.logger.write(level, fmt.Sprintf(
		"event=stage_finished site=%q stage=%q status=%q completed_bytes=%d total_bytes=%d duration_ms=%d",
		p.site, p.stage, status, p.completed, p.total, time.Since(p.stageStart).Milliseconds(),
	))
	p.stageActive = false
}
