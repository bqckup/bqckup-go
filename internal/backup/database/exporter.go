package database

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bqckup/bqckup-go/internal/apperror"
	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
)

type ProcessExporter struct {
	process     process.ProcessRunner
	command     string
	passwordEnv string
	engine      string
}

func NewMySQL(runner process.ProcessRunner) *ProcessExporter {
	return &ProcessExporter{process: runner, command: "mysqldump", passwordEnv: "MYSQL_PWD", engine: "mysql"}
}

func NewPostgres(runner process.ProcessRunner) *ProcessExporter {
	return &ProcessExporter{process: runner, command: "pg_dump", passwordEnv: "PGPASSWORD", engine: "postgres"}
}

func (e *ProcessExporter) Preflight() error {
	if _, err := e.process.LookPath(e.command); err != nil {
		return apperror.Hide("required database exporter is unavailable", err)
	}
	return nil
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
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return backup.Package{}, apperror.Hide("could not prepare database export", err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backup.Package{}, apperror.Hide("could not create database export", err)
	}
	success := false
	defer func() {
		if !success {
			_ = output.Close()
			_ = os.Remove(destination)
		}
	}()

	gzipWriter := gzip.NewWriter(output)
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
			return backup.Package{}, ctxErr
		}
		return backup.Package{}, apperror.Hide("could not export database", processErr)
	}
	if gzipErr != nil {
		return backup.Package{}, apperror.Hide("could not finish database export", gzipErr)
	}
	if err := output.Sync(); err != nil {
		return backup.Package{}, apperror.Hide("could not sync database export", err)
	}
	if err := output.Close(); err != nil {
		return backup.Package{}, apperror.Hide("could not close database export", err)
	}

	checksum, size, err := backup.ChecksumFile(destination)
	if err != nil {
		return backup.Package{}, err
	}
	success = true
	return backup.Package{
		Path:       destination,
		Size:       size,
		SHA256:     checksum,
		SourceKind: "database",
		SourceName: source.Name,
	}, nil
}

func (e *ProcessExporter) arguments(source config.DatabaseSource) []string {
	port := strconv.Itoa(source.Port)
	if e.engine == "mysql" {
		return []string{
			"--host=" + source.Host,
			"--port=" + port,
			"--user=" + source.Username,
			"--single-transaction",
			"--quick",
			"--routines",
			"--triggers",
			source.Database,
		}
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
			"--host=" + source.Host,
			"--port=" + port,
			"--user=" + source.Username,
			"--no-data",
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
