package database

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
)

type ProcessExporter struct {
	process     ProcessRunner
	command     string
	passwordEnv string
	engine      string
}

func NewMySQL(process ProcessRunner) *ProcessExporter {
	return &ProcessExporter{process: process, command: "mysqldump", passwordEnv: "MYSQL_PWD", engine: "mysql"}
}

func NewPostgres(process ProcessRunner) *ProcessExporter {
	return &ProcessExporter{process: process, command: "pg_dump", passwordEnv: "PGPASSWORD", engine: "postgres"}
}

func (e *ProcessExporter) Preflight() error {
	if _, err := e.process.LookPath(e.command); err != nil {
		return hiddenError("required database exporter is unavailable", err)
	}
	return nil
}

func (e *ProcessExporter) Export(ctx context.Context, source config.DatabaseSource, destination string) (artifact backup.Artifact, err error) {
	if err := ctx.Err(); err != nil {
		return backup.Artifact{}, err
	}
	if source.Engine != e.engine {
		return backup.Artifact{}, errors.New("database exporter does not match source engine")
	}
	if err := e.Preflight(); err != nil {
		return backup.Artifact{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return backup.Artifact{}, hiddenError("could not prepare database export", err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backup.Artifact{}, hiddenError("could not create database export", err)
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
	processErr := e.process.Run(ctx, ProcessSpec{
		Command: e.command,
		Args:    e.arguments(source),
		Env:     []string{e.passwordEnv + "=" + source.Password},
		Stdout:  gzipWriter,
		Stderr:  &stderr,
	})
	gzipErr := gzipWriter.Close()
	if processErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return backup.Artifact{}, ctxErr
		}
		return backup.Artifact{}, hiddenError("could not export database", processErr)
	}
	if gzipErr != nil {
		return backup.Artifact{}, hiddenError("could not finish database export", gzipErr)
	}
	if err := output.Sync(); err != nil {
		return backup.Artifact{}, hiddenError("could not sync database export", err)
	}
	if err := output.Close(); err != nil {
		return backup.Artifact{}, hiddenError("could not close database export", err)
	}

	checksum, size, err := checksumFile(destination)
	if err != nil {
		return backup.Artifact{}, err
	}
	success = true
	return backup.Artifact{
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

func checksumFile(filename string) (string, int64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", 0, hiddenError("could not verify database export", err)
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", 0, hiddenError("could not verify database export", copyErr)
	}
	if closeErr != nil {
		return "", 0, hiddenError("could not verify database export", closeErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }
func (e *redactedError) Unwrap() error { return e.cause }

func hiddenError(message string, cause error) error {
	return &redactedError{message: message, cause: cause}
}

var _ backup.Exporter = (*ProcessExporter)(nil)
