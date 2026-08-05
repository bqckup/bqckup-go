package database

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMySQLExporterWritesCompressedVerifiedArtifact(t *testing.T) {
	runner := &fakeProcessRunner{output: "CREATE DATABASE application;\n"}
	exporter := NewMySQL(runner)
	source := validDatabaseSource("mysql")
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	artifact, err := exporter.Export(context.Background(), source, destination)
	require.NoError(t, err)
	assert.Equal(t, "mysqldump", runner.spec.Command)
	assert.Contains(t, runner.spec.Args, "--single-transaction")
	assert.Contains(t, runner.spec.Args, "--quick")
	assert.Contains(t, runner.spec.Args, "--routines")
	assert.Contains(t, runner.spec.Args, "--triggers")
	assert.NotContains(t, strings.Join(runner.spec.Args, " "), source.Password)
	assert.Equal(t, source.Password, environmentValue(runner.spec.Env, "MYSQL_PWD"))
	assert.Equal(t, "database", artifact.SourceKind)
	assert.Equal(t, source.Name, artifact.SourceName)
	assert.Equal(t, destination, artifact.Path)
	compressed, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, int64(len(compressed)), artifact.Size)
	assert.Equal(t, checksum(string(compressed)), artifact.SHA256)

	file, err := os.Open(destination)
	require.NoError(t, err)
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	contents, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.NoError(t, file.Close())
	assert.Equal(t, "CREATE DATABASE application;\n", string(contents))
	info, err := os.Stat(destination)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestPostgresExporterUsesPGPASSWORD(t *testing.T) {
	runner := &fakeProcessRunner{output: "CREATE TABLE application;\n"}
	exporter := NewPostgres(runner)
	source := validDatabaseSource("postgres")
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	artifact, err := exporter.Export(context.Background(), source, destination)
	require.NoError(t, err)
	assert.Equal(t, "pg_dump", runner.spec.Command)
	assert.Contains(t, runner.spec.Args, "--format=plain")
	assert.Contains(t, runner.spec.Args, "--no-owner")
	assert.Contains(t, runner.spec.Args, "--no-privileges")
	assert.Equal(t, source.Password, environmentValue(runner.spec.Env, "PGPASSWORD"))
	compressed, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, checksum(string(compressed)), artifact.SHA256)
}

func TestExporterRemovesPartialArtifactOnFailure(t *testing.T) {
	runner := &fakeProcessRunner{
		err:    errors.New("process failed with password database-secret"),
		stderr: "database-secret provider output",
	}
	exporter := NewMySQL(runner)
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	_, err := exporter.Export(context.Background(), validDatabaseSource("mysql"), destination)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "database-secret")
	_, statErr := os.Stat(destination)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestExporterRemovesArtifactOnCancellation(t *testing.T) {
	runner := &fakeProcessRunner{waitForCancel: true}
	exporter := NewPostgres(runner)
	destination := filepath.Join(t.TempDir(), "application.sql.gz")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := exporter.Export(ctx, validDatabaseSource("postgres"), destination)
	require.ErrorIs(t, err, context.Canceled)
	_, statErr := os.Stat(destination)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

type fakeProcessRunner struct {
	spec          ProcessSpec
	output        string
	stderr        string
	err           error
	waitForCancel bool
}

func (f *fakeProcessRunner) LookPath(command string) (string, error) { return command, nil }

func (f *fakeProcessRunner) Run(ctx context.Context, spec ProcessSpec) error {
	f.spec = spec
	if f.waitForCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	_, _ = io.WriteString(spec.Stdout, f.output)
	_, _ = io.WriteString(spec.Stderr, f.stderr)
	return f.err
}

func validDatabaseSource(engine string) config.DatabaseSource {
	return config.DatabaseSource{
		Name: "application-" + engine, Enabled: true, Engine: engine,
		Host: "localhost", Port: 3306, Database: "application",
		Username: "backup-user", Password: "database-secret",
	}
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}

func checksum(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

var _ backup.Exporter = (*ProcessExporter)(nil)
