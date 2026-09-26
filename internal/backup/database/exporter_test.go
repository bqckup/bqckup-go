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
	"time"

	"github.com/bqckup/bqckup-go/internal/backup"
	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMySQLExporterWritesCompressedVerifiedPackage(t *testing.T) {
	runner := &fakeProcessRunner{output: "CREATE DATABASE application;\n"}
	exporter := NewMySQL(runner)
	source := validDatabaseSource("mysql")
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	pkg, err := exporter.Export(context.Background(), source, destination)
	require.NoError(t, err)
	assert.Equal(t, "mysqldump", runner.spec.Command)
	assert.Contains(t, runner.spec.Args, "--single-transaction")
	assert.Contains(t, runner.spec.Args, "--quick")
	assert.Contains(t, runner.spec.Args, "--routines")
	assert.Contains(t, runner.spec.Args, "--triggers")
	assert.Contains(t, runner.spec.Args, "--events")
	assert.NotContains(t, strings.Join(runner.spec.Args, " "), source.Password)
	assert.Equal(t, source.Password, environmentValue(runner.spec.Env, "MYSQL_PWD"))
	assert.Equal(t, "database", pkg.SourceKind)
	assert.Equal(t, source.Name, pkg.SourceName)
	assert.Equal(t, destination, pkg.Path)
	compressed, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, int64(len(compressed)), pkg.Size)
	assert.Equal(t, checksum(string(compressed)), pkg.SHA256)

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

	pkg, err := exporter.Export(context.Background(), source, destination)
	require.NoError(t, err)
	assert.Equal(t, "pg_dump", runner.spec.Command)
	assert.Contains(t, runner.spec.Args, "--format=plain")
	assert.Contains(t, runner.spec.Args, "--no-owner")
	assert.Contains(t, runner.spec.Args, "--no-privileges")
	assert.Equal(t, source.Password, environmentValue(runner.spec.Env, "PGPASSWORD"))
	compressed, err := os.ReadFile(destination)
	require.NoError(t, err)
	assert.Equal(t, checksum(string(compressed)), pkg.SHA256)
}

func TestExporterRemovesPartialPackageOnFailure(t *testing.T) {
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

func TestExporterRetriesTransientFailureBeforeSuccess(t *testing.T) {
	runner := &fakeProcessRunner{
		errs:    []error{errors.New("exit status 1"), nil},
		stderrs: []string{"mysqldump: Got error: 2013 Lost connection to MySQL server during query\n", ""},
		output:  "CREATE DATABASE application;\n",
	}
	exporter := NewMySQL(runner)
	exporter.retryDelays = []time.Duration{0}
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	_, err := exporter.Export(context.Background(), validDatabaseSource("mysql"), destination)

	require.NoError(t, err)
	assert.Equal(t, 2, runner.calls)
}

func TestExporterDoesNotRetryPermanentFailure(t *testing.T) {
	runner := &fakeProcessRunner{
		errs:    []error{errors.New("exit status 1"), nil},
		stderrs: []string{"mysqldump: Got error: 1045: Access denied for user\n", ""},
	}
	exporter := NewMySQL(runner)
	exporter.retryDelays = []time.Duration{0}
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	_, err := exporter.Export(context.Background(), validDatabaseSource("mysql"), destination)

	require.Error(t, err)
	assert.Equal(t, 1, runner.calls)
}

func TestExporterAutoRepairsCorruptionOnlyWhenOptedIn(t *testing.T) {
	runner := &fakeProcessRunner{
		errs:    []error{errors.New("exit status 1"), nil},
		stderrs: []string{"mysqldump: Error 145: Table is marked as crashed", ""},
		output:  "CREATE DATABASE application;\n",
	}
	repairer := &fakeAutoRepairer{result: AutoRepairResult{Repaired: []RepairResult{{Table: "cache", Healthy: true}}}}
	exporter := NewMySQL(runner)
	exporter.SetAutoRepairer(repairer)
	exporter.retryDelays = nil
	source := validDatabaseSource("mysql")
	source.AutoRepair = true
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	_, err := exporter.Export(context.Background(), source, destination)

	require.NoError(t, err)
	assert.Equal(t, 2, runner.calls)
	assert.Equal(t, 1, repairer.calls)
}

func TestExporterDoesNotAutoRepairByDefault(t *testing.T) {
	runner := &fakeProcessRunner{err: errors.New("exit status 1"), stderr: "Table is marked as crashed"}
	repairer := &fakeAutoRepairer{result: AutoRepairResult{Repaired: []RepairResult{{Table: "cache", Healthy: true}}}}
	exporter := NewMySQL(runner)
	exporter.SetAutoRepairer(repairer)
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	_, err := exporter.Export(context.Background(), validDatabaseSource("mysql"), destination)

	require.Error(t, err)
	assert.Equal(t, 0, repairer.calls)
	assert.Equal(t, 1, runner.calls)
}

func TestExporterDoesNotAutoRepairTransientFailure(t *testing.T) {
	runner := &fakeProcessRunner{
		errs:    []error{errors.New("exit status 1"), nil},
		stderrs: []string{"mysqldump: timed out while table is marked as crashed", ""},
		output:  "CREATE DATABASE application;\n",
	}
	repairer := &fakeAutoRepairer{result: AutoRepairResult{Repaired: []RepairResult{{Table: "cache", Healthy: true}}}}
	exporter := NewMySQL(runner)
	exporter.SetAutoRepairer(repairer)
	exporter.retryDelays = []time.Duration{0}
	source := validDatabaseSource("mysql")
	source.AutoRepair = true
	destination := filepath.Join(t.TempDir(), "application.sql.gz")

	_, err := exporter.Export(context.Background(), source, destination)

	require.NoError(t, err)
	assert.Equal(t, 0, repairer.calls)
	assert.Equal(t, 2, runner.calls)
}

func TestExporterRemovesPackageOnCancellation(t *testing.T) {
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
	spec          process.ProcessSpec
	output        string
	stderr        string
	err           error
	errs          []error
	stderrs       []string
	calls         int
	waitForCancel bool
}

type fakeAutoRepairer struct {
	calls  int
	result AutoRepairResult
	err    error
}

func (f *fakeAutoRepairer) RepairCorruptTables(context.Context, config.DatabaseSource) (AutoRepairResult, error) {
	f.calls++
	return f.result, f.err
}

func TestProcessExporterProbeMySQL(t *testing.T) {
	runner := &fakeProcessRunner{}
	exporter := NewMySQL(runner)
	source := validDatabaseSource("mysql")

	require.NoError(t, exporter.Probe(context.Background(), source))
	assert.Equal(t, "mysqldump", runner.spec.Command)
	assert.Contains(t, runner.spec.Args, "--host=localhost")
	assert.Contains(t, runner.spec.Args, "--port=3306")
	assert.Contains(t, runner.spec.Args, "--user=backup-user")
	assert.Contains(t, runner.spec.Args, "--no-data")
	assert.Contains(t, runner.spec.Args, "application")
	assert.Contains(t, runner.spec.Args, "--single-transaction")
	assert.Contains(t, runner.spec.Args, "--quick")
	assert.NotContains(t, strings.Join(runner.spec.Args, " "), source.Password)
	assert.Equal(t, source.Password, environmentValue(runner.spec.Env, "MYSQL_PWD"))
	assert.Equal(t, io.Discard, runner.spec.Stdout, "probe output must be discarded")
}

func TestProcessExporterProbePostgres(t *testing.T) {
	runner := &fakeProcessRunner{}
	exporter := NewPostgres(runner)
	source := validDatabaseSource("postgres")

	require.NoError(t, exporter.Probe(context.Background(), source))
	assert.Equal(t, "pg_dump", runner.spec.Command)
	assert.Contains(t, runner.spec.Args, "--schema-only")
	assert.Contains(t, runner.spec.Args, "--username=backup-user")
	assert.Contains(t, runner.spec.Args, "application")
	assert.NotContains(t, runner.spec.Args, "--format=plain")
	assert.NotContains(t, runner.spec.Args, "--no-owner")
	assert.Equal(t, source.Password, environmentValue(runner.spec.Env, "PGPASSWORD"))
	assert.Equal(t, io.Discard, runner.spec.Stdout)
}

func TestProcessExporterProbeReportsFirstStderrLine(t *testing.T) {
	runner := &fakeProcessRunner{
		err:    errors.New("exit status 1"),
		stderr: "mysqldump: Got error: 1045: Access denied for user 'x' (using password: YES)\nExtra line\n",
	}
	exporter := NewMySQL(runner)

	err := exporter.Probe(context.Background(), validDatabaseSource("mysql"))
	require.Error(t, err)
	assert.Equal(t, "mysqldump: Got error: 1045: Access denied for user 'x' (using password: YES)", err.Error())
}

func TestProcessExporterProbeTruncatesLongStderrLines(t *testing.T) {
	runner := &fakeProcessRunner{err: errors.New("exit status 1"), stderr: strings.Repeat("x", 500) + "\n"}
	exporter := NewMySQL(runner)

	err := exporter.Probe(context.Background(), validDatabaseSource("mysql"))
	require.Error(t, err)
	assert.LessOrEqual(t, len(err.Error()), 200)
}

func TestProcessExporterProbeFallsBackWhenStderrEmpty(t *testing.T) {
	runner := &fakeProcessRunner{err: errors.New("exit status 1")}
	exporter := NewMySQL(runner)

	err := exporter.Probe(context.Background(), validDatabaseSource("mysql"))
	require.Error(t, err)
	assert.Equal(t, "database connection check failed", err.Error())
}

func TestProcessExporterProbeRejectsEngineMismatch(t *testing.T) {
	exporter := NewMySQL(&fakeProcessRunner{})

	err := exporter.Probe(context.Background(), validDatabaseSource("postgres"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match source engine")
}

func TestProcessExporterProbeCancelledContextSkipsRun(t *testing.T) {
	runner := &fakeProcessRunner{}
	exporter := NewPostgres(runner)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := exporter.Probe(ctx, validDatabaseSource("postgres"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, "", runner.spec.Command, "runner must not be called")
}

func TestProcessExporterProbeReturnsContextErrorOnLateCancel(t *testing.T) {
	runner := &fakeProcessRunner{waitForCancel: true}
	exporter := NewPostgres(runner)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() {
		done <- exporter.Probe(ctx, validDatabaseSource("postgres"))
	}()
	cancel()

	require.ErrorIs(t, <-done, context.Canceled)
}

func (f *fakeProcessRunner) LookPath(command string) (string, error) { return command, nil }

func (f *fakeProcessRunner) Run(ctx context.Context, spec process.ProcessSpec) error {
	f.spec = spec
	f.calls++
	if f.waitForCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	err := f.err
	stderr := f.stderr
	if len(f.errs) > 0 {
		index := f.calls - 1
		if index >= len(f.errs) {
			index = len(f.errs) - 1
		}
		err = f.errs[index]
	}
	if len(f.stderrs) > 0 {
		index := f.calls - 1
		if index >= len(f.stderrs) {
			index = len(f.stderrs) - 1
		}
		stderr = f.stderrs[index]
	}
	_, _ = io.WriteString(spec.Stdout, f.output)
	_, _ = io.WriteString(spec.Stderr, stderr)
	return err
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
