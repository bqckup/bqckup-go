package database

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
)

type ProcessExporter struct {
	process      process.ProcessRunner
	command      string
	passwordEnv  string
	engine       string
	retryDelays  []time.Duration
	autoRepairer AutoRepairer
}

func NewMySQL(runner process.ProcessRunner) *ProcessExporter {
	return &ProcessExporter{
		process: runner, command: "mysqldump", passwordEnv: "MYSQL_PWD", engine: "mysql",
		retryDelays: []time.Duration{15 * time.Second, 60 * time.Second},
	}
}

func NewPostgres(runner process.ProcessRunner) *ProcessExporter {
	return &ProcessExporter{
		process: runner, command: "pg_dump", passwordEnv: "PGPASSWORD", engine: "postgres",
		retryDelays: []time.Duration{15 * time.Second, 60 * time.Second},
	}
}

// SetAutoRepairer wires the optional source-database maintenance capability.
// It is intentionally separate from the constructor so existing library
// callers and tests keep the safe default of no automatic repair.
func (e *ProcessExporter) SetAutoRepairer(repairer AutoRepairer) {
	e.autoRepairer = repairer
}

func (e *ProcessExporter) Preflight() error {
	if _, err := e.process.LookPath(e.command); err != nil {
		return apperror.Hide("required database exporter is unavailable", err)
	}
	return nil
}

func (e *ProcessExporter) EstimateSize(ctx context.Context, source config.DatabaseSource) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if source.Engine != e.engine {
		return 0, false, errors.New("database exporter does not match source engine")
	}
	if err := e.Preflight(); err != nil {
		return 0, false, err
	}

	probeCommand := "mysql"
	probeArgs := []string{
		"--protocol=TCP",
		"--host=" + source.Host,
		"--port=" + strconv.Itoa(source.Port),
		"--user=" + source.Username,
		"--batch",
		"--skip-column-names",
		"-e",
		"SELECT COALESCE(SUM(data_length + index_length), 0) FROM information_schema.tables WHERE table_schema = DATABASE();",
	}
	if e.engine == "postgres" {
		probeCommand = "psql"
		probeArgs = []string{
			"--host=" + source.Host,
			"--port=" + strconv.Itoa(source.Port),
			"--username=" + source.Username,
			"--tuples-only",
			"--no-align",
			"-c",
			"SELECT pg_database_size(current_database());",
			source.Database,
		}
	}

	if _, err := e.process.LookPath(probeCommand); err != nil {
		return 0, false, nil
	}

	var stdout, stderr bytes.Buffer
	processErr := e.process.Run(ctx, process.ProcessSpec{
		Command: probeCommand,
		Args:    probeArgs,
		Env:     []string{e.passwordEnv + "=" + source.Password},
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	if processErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, false, ctxErr
		}
		return 0, false, nil
	}

	value := strings.TrimSpace(stdout.String())
	if value == "" {
		return 0, false, nil
	}
	size, err := strconv.ParseInt(value, 10, 64)
	if err != nil || size <= 0 {
		return 0, false, nil
	}
	return size, true, nil
}

func (e *ProcessExporter) Export(ctx context.Context, source config.DatabaseSource, destination string) (backup.Package, error) {
	if err := ctx.Err(); err != nil {
		return backup.Package{}, err
	}
	if source.Engine != e.engine {
		return backup.Package{}, errors.New("database exporter does not match source engine")
	}
	if err := e.Preflight(); err != nil {
		return backup.Package{}, err
	}
	repaired := false
	var repairWarnings []string
	for attempt := 0; ; attempt++ {
		pkg, err, retryable, corruption := e.exportOnce(ctx, source, destination)
		if err != nil && source.AutoRepair && corruption && !repaired {
			repaired = true
			if e.autoRepairer == nil {
				return backup.Package{}, errors.Join(err, errors.New("automatic database repair is enabled but unavailable"))
			}
			repairResult, repairErr := e.autoRepairer.RepairCorruptTables(ctx, source)
			if repairErr != nil {
				return backup.Package{}, errors.Join(err, fmt.Errorf("automatic database repair failed: %w", repairErr))
			}
			if len(repairResult.Repaired) == 0 {
				return backup.Package{}, errors.Join(err, errors.New("automatic database repair found no repairable table"))
			}
			for _, repair := range repairResult.Repaired {
				repairWarnings = append(repairWarnings, fmt.Sprintf("automatic database repair applied to table %q (%s): %s", repair.Table, repair.Engine, repair.Message))
			}
			continue
		}
		if err == nil || !retryable || attempt >= len(e.retryDelays) {
			if err == nil {
				pkg.Warnings = append(pkg.Warnings, repairWarnings...)
			}
			return pkg, err
		}
		if err := waitForRetry(ctx, e.retryDelays[attempt]); err != nil {
			return backup.Package{}, err
		}
	}
}

func (e *ProcessExporter) exportOnce(ctx context.Context, source config.DatabaseSource, destination string) (backup.Package, error, bool, bool) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return backup.Package{}, apperror.Hide("could not prepare database export", err), false, false
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backup.Package{}, apperror.Hide("could not create database export", err), false, false
	}
	success := false
	defer func() {
		if !success {
			_ = output.Close()
			_ = os.Remove(destination)
		}
	}()

	digest := sha256.New()
	gzipWriter := gzip.NewWriter(io.MultiWriter(output, digest))
	var stderr bytes.Buffer
	processErr := e.process.Run(ctx, process.ProcessSpec{
		Command: e.command,
		Args:    e.arguments(source),
		Env:     []string{e.passwordEnv + "=" + source.Password},
		Stdout:  gzipWriter,
		Stderr:  &stderr,
	})
	gzipErr := gzipWriter.Close()
	if processErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return backup.Package{}, ctxErr, false, false
		}
		transient := isTransientDatabaseError(processErr, stderr.String())
		return backup.Package{}, apperror.Hide("could not export database", processErr), transient, !transient && isCorruptionDatabaseError(processErr, stderr.String())
	}
	if gzipErr != nil {
		return backup.Package{}, apperror.Hide("could not finish database export", gzipErr), false, false
	}
	if err := output.Sync(); err != nil {
		return backup.Package{}, apperror.Hide("could not sync database export", err), false, false
	}
	info, err := output.Stat()
	if err != nil {
		return backup.Package{}, apperror.Hide("could not stat database export", err), false, false
	}
	if err := output.Close(); err != nil {
		return backup.Package{}, apperror.Hide("could not close database export", err), false, false
	}

	success = true
	return backup.Package{
		Path:       destination,
		Size:       info.Size(),
		SHA256:     hex.EncodeToString(digest.Sum(nil)),
		SourceKind: "database",
		SourceName: source.Name,
	}, nil, false, false
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isTransientDatabaseError(processErr error, stderr string) bool {
	if processErr == nil {
		return false
	}
	message := strings.ToLower(processErr.Error() + "\n" + stderr)
	for _, marker := range []string{
		"timeout",
		"timed out",
		"deadlock",
		"lock wait",
		"too many connections",
		"server has gone away",
		"lost connection",
		"connection reset",
		"connection refused",
		"can't connect",
		"cannot connect",
		"temporary failure",
		"try again",
		"resource temporarily unavailable",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func isCorruptionDatabaseError(processErr error, stderr string) bool {
	if processErr == nil {
		return false
	}
	message := strings.ToLower(processErr.Error() + "\n" + stderr)
	for _, marker := range []string{
		"table is marked as crashed",
		"marked as crashed",
		"incorrect key file",
		"table is corrupted",
		"table corrupt",
		"corrupt table",
		"checksum mismatch",
		"error: 1034",
		"error 1034",
		"error: 126",
		"error 126",
		"error: 127",
		"error 127",
		"error: 134",
		"error 134",
		"error: 135",
		"error 135",
		"error: 145",
		"error 145",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (e *ProcessExporter) arguments(source config.DatabaseSource) []string {
	port := strconv.Itoa(source.Port)
	if e.engine == "mysql" {
		args := []string{
			"--no-defaults",
			"--host=" + source.Host,
			"--port=" + port,
			"--user=" + source.Username,
			"--single-transaction",
			"--quick",
			"--routines",
			"--triggers",
		}
		if !source.SkipEvents {
			args = append(args, "--events")
		}
		return append(args, source.Database)
	}
	return []string{
		"--host=" + source.Host,
		"--port=" + port,
		"--username=" + source.Username,
		"--format=plain",
		"--no-owner",
		"--no-privileges",
		source.Database,
	}
}

// Probe verifies the database connection with the dump binary in read-only
// mode (--no-data / --schema-only), discarding stdout and passing the
// password only through the child environment. Nothing is written to disk.
// The returned error text is the first non-empty stderr line, truncated to
// 200 bytes, so it is safe to print as a check message.
func (e *ProcessExporter) Probe(ctx context.Context, source config.DatabaseSource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if source.Engine != e.engine {
		return errors.New("database exporter does not match source engine")
	}
	if err := e.Preflight(); err != nil {
		return err
	}
	var stderr bytes.Buffer
	processErr := e.process.Run(ctx, process.ProcessSpec{
		Command: e.command,
		Args:    e.probeArguments(source),
		Env:     []string{e.passwordEnv + "=" + source.Password},
		Stdout:  io.Discard,
		Stderr:  &stderr,
	})
	if processErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if message := firstStderrLine(stderr.String()); message != "" {
			return errors.New(message)
		}
		return errors.New("database connection check failed")
	}
	return nil
}

func (e *ProcessExporter) probeArguments(source config.DatabaseSource) []string {
	port := strconv.Itoa(source.Port)
	if e.engine == "mysql" {
		return []string{
			"--no-defaults",
			"--host=" + source.Host,
			"--port=" + port,
			"--user=" + source.Username,
			"--no-data",
			"--single-transaction",
			"--quick",
			source.Database,
		}
	}
	return []string{
		"--host=" + source.Host,
		"--port=" + port,
		"--username=" + source.Username,
		"--schema-only",
		source.Database,
	}
}

// firstStderrLine returns the first non-empty stderr line, trimmed and
// truncated to 200 bytes without splitting a rune.
func firstStderrLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) <= 200 {
			return line
		}
		runes := []rune(line)
		total := 0
		for i, r := range runes {
			size := utf8.RuneLen(r)
			if total+size > 200 {
				return string(runes[:i])
			}
			total += size
		}
		return line
	}
	return ""
}

var _ backup.Exporter = (*ProcessExporter)(nil)
