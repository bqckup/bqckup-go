package database

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"

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
		return backup.Artifact{}, apperror.Hide("could not prepare database export", err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backup.Artifact{}, apperror.Hide("could not create database export", err)
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
			return backup.Artifact{}, ctxErr
		}
		return backup.Artifact{}, apperror.Hide("could not export database", processErr)
	}
	if gzipErr != nil {
		return backup.Artifact{}, apperror.Hide("could not finish database export", gzipErr)
	}
	if err := output.Sync(); err != nil {
		return backup.Artifact{}, apperror.Hide("could not sync database export", err)
	}
	if err := output.Close(); err != nil {
		return backup.Artifact{}, apperror.Hide("could not close database export", err)
	}

	checksum, size, err := backup.ChecksumFile(destination)
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

var _ backup.Exporter = (*ProcessExporter)(nil)
