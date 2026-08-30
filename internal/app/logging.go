package app

import (
	"io"
	"log"
	"os"
	"path/filepath"

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
