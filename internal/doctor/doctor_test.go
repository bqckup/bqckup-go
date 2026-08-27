package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bqckup/bqckup-go/internal/config"
	"github.com/bqckup/bqckup-go/internal/process"
	"github.com/bqckup/bqckup-go/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner implements process.ProcessRunner with a configurable PATH.
type fakeRunner struct {
	paths map[string]string
}

func (f *fakeRunner) LookPath(command string) (string, error) {
	if f.paths != nil {
		if found, ok := f.paths[command]; ok {
			return found, nil
		}
	}
	return "", os.ErrNotExist
}

func (f *fakeRunner) Run(context.Context, process.ProcessSpec) error { return nil }

// testConfig builds a valid config whose app directories live under a
// fresh temp dir so the directory checks pass and nothing escapes the test.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	return &config.Config{
		Version: 2,
		App: config.App{
			StateDatabase:      filepath.Join(root, "data", "state.db"),
			TemporaryDirectory: filepath.Join(root, "tmp"),
			LockDirectory:      filepath.Join(root, "locks"),
		},
		Storages: map[string]config.Storage{"local-primary": {Type: "local"}},
		Sites: []config.Site{{
			Name: "test-site", Enabled: true, BackupMode: "incremental",
			Incremental:   config.Incremental{PasswordEnv: "TEST_RESTIC_PASS"},
			Destinations:  []config.Destination{{Storage: "local-primary"}},
			SchemaVersion: 2,
		}},
	}
}

func TestRunPassesOnValidConfiguration(t *testing.T) {
	t.Setenv("TEST_RESTIC_PASS", "secret-value")
	checker := &Checker{Cfg: testConfig(t), Runner: &fakeRunner{paths: map[string]string{}},
		Stores: map[string]storage.Store{"local-primary": &fakeProbeStore{}}}

	report, err := checker.Run(context.Background(), "")
	require.NoError(t, err)
	assert.True(t, report.Passed)
	var names []string
	for _, check := range report.Checks {
		names = append(names, check.Name)
		assert.Equal(t, "ok", check.Status, check.Name)
	}
	assert.Equal(t, []string{"config", "temp_dir", "lock_dir", "state_db_dir", "engine:test-site", "secret:test-site:TEST_RESTIC_PASS", "storage:local-primary"}, names)
}

func TestRunFailsWhenPasswordEnvIsMissing(t *testing.T) {
	_ = os.Unsetenv("UNSET_DOCTOR_PASS_VAR")
	cfg := testConfig(t)
	cfg.Sites[0].Incremental.PasswordEnv = "UNSET_DOCTOR_PASS_VAR"
	checker := &Checker{Cfg: cfg, Runner: &fakeRunner{paths: map[string]string{}},
		Stores: map[string]storage.Store{"local-primary": &fakeProbeStore{}}}

	report, err := checker.Run(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, report.Passed)
	assert.Equal(t, "fail", checkStatus(report, "secret:test-site:UNSET_DOCTOR_PASS_VAR"))
}

func TestRunBinaryChecksFollowLookPath(t *testing.T) {
	t.Setenv("TEST_RESTIC_PASS", "secret-value")
	cfg := testConfig(t)
	cfg.Sites[0].BackupMode = "full"
	cfg.Sites[0].Sources.Databases = []config.DatabaseSource{
		{Name: "db", Enabled: true, Engine: "mysql"},
		{Name: "db", Enabled: true, Engine: "postgres"},
	}

	t.Run("reports both binaries when found", func(t *testing.T) {
		runner := &fakeRunner{paths: map[string]string{"mysqldump": "/usr/bin/mysqldump", "pg_dump": "/usr/bin/pg_dump"}}
		report, err := (&Checker{Cfg: cfg, Runner: runner,
			Stores:    map[string]storage.Store{"local-primary": &fakeProbeStore{}},
			DBProbers: map[string]DatabaseProber{"mysql": &fakeDBProber{}, "postgres": &fakeDBProber{}},
		}).Run(context.Background(), "")
		require.NoError(t, err)
		assert.True(t, report.Passed)
		assert.Equal(t, "ok", checkStatus(report, "binary:mysqldump"))
		assert.Equal(t, "ok", checkStatus(report, "binary:pg_dump"))
	})

	t.Run("fails when a binary is missing", func(t *testing.T) {
		runner := &fakeRunner{paths: map[string]string{"pg_dump": "/usr/bin/pg_dump"}}
		report, err := (&Checker{Cfg: cfg, Runner: runner,
			Stores:    map[string]storage.Store{"local-primary": &fakeProbeStore{}},
			DBProbers: map[string]DatabaseProber{"mysql": &fakeDBProber{}, "postgres": &fakeDBProber{}},
		}).Run(context.Background(), "")
		require.NoError(t, err)
		assert.False(t, report.Passed)
		assert.Equal(t, "fail", checkStatus(report, "binary:mysqldump"))
	})
}

func TestRunReportsConfigLoadFailure(t *testing.T) {
	checker := &Checker{LoadErr: errors.New("boom"), Runner: &fakeRunner{}}

	report, err := checker.Run(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, report.Checks, 1)
	assert.Equal(t, "config", report.Checks[0].Name)
	assert.Equal(t, "fail", report.Checks[0].Status)
	assert.Contains(t, report.Checks[0].Message, "could not load configuration: boom")
	assert.False(t, report.Passed)
}

func TestRunValidatesSiteFilter(t *testing.T) {
	t.Setenv("TEST_RESTIC_PASS", "secret-value")
	cfg := testConfig(t)
	cfg.Sites = append(cfg.Sites, config.Site{Name: "disabled-site", Enabled: false, SchemaVersion: 2})
	checker := &Checker{Cfg: cfg, Runner: &fakeRunner{paths: map[string]string{}}}

	_, err := checker.Run(context.Background(), "unknown-site")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `site "unknown-site" is not configured`)

	_, err = checker.Run(context.Background(), "disabled-site")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `site "disabled-site" is disabled`)
}

func checkStatus(report DoctorReport, name string) string {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}

// ---- T4: connectivity probe orchestration ----

// fakeProbeStore satisfies storage.Store and records how often Probe runs.
type fakeProbeStore struct {
	probeErr error
	probes   int
}

func (f *fakeProbeStore) Probe(context.Context) error {
	f.probes++
	return f.probeErr
}
func (f *fakeProbeStore) Put(context.Context, storage.Artifact, string) (storage.StoredArtifact, error) {
	return storage.StoredArtifact{}, errors.New("unused")
}
func (f *fakeProbeStore) Delete(context.Context, string) error { return errors.New("unused") }
func (f *fakeProbeStore) ListBackupSets(context.Context, string) ([]storage.BackupSet, error) {
	return nil, errors.New("unused")
}

// fakeDBProber satisfies doctor's DatabaseProber interface.
type fakeDBProber struct {
	probeErr   error
	blockOnCtx bool
	probed     []config.DatabaseSource
}

func (f *fakeDBProber) Probe(ctx context.Context, source config.DatabaseSource) error {
	f.probed = append(f.probed, source)
	if f.blockOnCtx {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.probeErr
}

// probeConfig returns a two-site config: site-a (full) with one mysql and
// one postgres source and destinations z-storage + a-storage; site-b
// (incremental) with one mysql source and destinations a-storage +
// m-storage. a-storage is shared, m-storage is only used by site-b.
func probeConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	cfg := testConfig(t)
	cfg.App.TemporaryDirectory = filepath.Join(root, "tmp")
	cfg.App.LockDirectory = filepath.Join(root, "locks")
	cfg.App.StateDatabase = filepath.Join(root, "data", "state.db")
	cfg.Storages = map[string]config.Storage{
		"a-storage": {Type: "local"},
		"m-storage": {Type: "local"},
		"z-storage": {Type: "local"},
	}
	cfg.Sites = []config.Site{
		{
			Name: "site-a", Enabled: true, BackupMode: "full", SchemaVersion: 2,
			Sources: config.Sources{Databases: []config.DatabaseSource{
				{Name: "db1", Enabled: true, Engine: "mysql", Username: "user-a1"},
				{Name: "db2", Enabled: true, Engine: "postgres", Username: "user-a2"},
				{Name: "db3", Enabled: false, Engine: "mysql"},
			}},
			Destinations: []config.Destination{{Storage: "z-storage"}, {Storage: "a-storage"}},
		},
		{
			Name: "site-b", Enabled: true, BackupMode: "incremental", SchemaVersion: 2,
			Incremental:  config.Incremental{PasswordEnv: "DOCTOR_PROBE_PASS"},
			Sources:      config.Sources{Databases: []config.DatabaseSource{{Name: "db1", Enabled: true, Engine: "mysql", Username: "user-b1"}}},
			Destinations: []config.Destination{{Storage: "a-storage"}, {Storage: "m-storage"}},
		},
	}
	return cfg
}

func probeChecker(t *testing.T, cfg *config.Config) (*Checker, *fakeDBProber, *fakeDBProber, map[string]*fakeProbeStore) {
	t.Helper()
	t.Setenv("DOCTOR_PROBE_PASS", "secret-value")
	mysqlProber := &fakeDBProber{}
	postgresProber := &fakeDBProber{}
	stores := map[string]*fakeProbeStore{
		"a-storage": {},
		"m-storage": {},
		"z-storage": {},
	}
	checker := &Checker{
		Cfg: cfg,
		Runner: &fakeRunner{paths: map[string]string{
			"mysqldump": "/usr/bin/mysqldump",
			"pg_dump":   "/usr/bin/pg_dump",
		}},
		DBProbers: map[string]DatabaseProber{"mysql": mysqlProber, "postgres": postgresProber},
		Stores: map[string]storage.Store{
			"a-storage": stores["a-storage"],
			"m-storage": stores["m-storage"],
			"z-storage": stores["z-storage"],
		},
	}
	return checker, mysqlProber, postgresProber, stores
}

func TestRunProbesHealthyConfiguration(t *testing.T) {
	cfg := probeConfig(t)
	checker, mysqlProber, postgresProber, stores := probeChecker(t, cfg)

	report, err := checker.Run(context.Background(), "")
	require.NoError(t, err)
	assert.True(t, report.Passed)

	var names []string
	for _, check := range report.Checks {
		names = append(names, check.Name)
	}
	assert.Equal(t, []string{
		"config", "temp_dir", "lock_dir", "state_db_dir",
		"engine:site-b", "secret:site-b:DOCTOR_PROBE_PASS",
		"binary:mysqldump", "binary:pg_dump",
		"database:site-a:db1", "database:site-a:db2", "database:site-b:db1",
		"storage:a-storage", "storage:m-storage", "storage:z-storage",
	}, names)

	assert.Equal(t, "ok", checkStatus(report, "database:site-a:db1"))
	assert.Equal(t, "connection ok", checkMessage(report, "database:site-b:db1"))
	assert.Equal(t, "access ok", checkMessage(report, "storage:a-storage"))

	// Probes ran in config order for databases and alphabetical for storages;
	// the shared a-storage was probed exactly once.
	require.Len(t, mysqlProber.probed, 2)
	assert.Equal(t, "user-a1", mysqlProber.probed[0].Username)
	assert.Equal(t, "user-b1", mysqlProber.probed[1].Username)
	require.Len(t, postgresProber.probed, 1)
	assert.Equal(t, "user-a2", postgresProber.probed[0].Username)
	assert.Equal(t, 1, stores["a-storage"].probes)
	assert.Equal(t, 1, stores["m-storage"].probes)
	assert.Equal(t, 1, stores["z-storage"].probes)
}

func TestRunReportsProbeAndConstructionFailures(t *testing.T) {
	cfg := probeConfig(t)
	checker, mysqlProber, _, _ := probeChecker(t, cfg)
	mysqlProber.probeErr = errors.New("connection refused")
	checker.StoreErrs = map[string]error{"m-storage": errors.New("could not prepare a storage destination")}
	delete(checker.Stores, "m-storage")

	report, err := checker.Run(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, report.Passed)
	assert.Equal(t, "fail", checkStatus(report, "database:site-a:db1"))
	assert.Equal(t, "connection refused", checkMessage(report, "database:site-a:db1"))
	assert.Equal(t, "fail", checkStatus(report, "storage:m-storage"))
	assert.Equal(t, "could not prepare a storage destination", checkMessage(report, "storage:m-storage"))
	assert.Equal(t, "ok", checkStatus(report, "storage:a-storage"))
}

func TestRunSkipsDatabaseProbesWhenBinaryMissing(t *testing.T) {
	cfg := probeConfig(t)
	checker, mysqlProber, _, _ := probeChecker(t, cfg)
	checker.Runner = &fakeRunner{paths: map[string]string{"pg_dump": "/usr/bin/pg_dump"}}

	report, err := checker.Run(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, report.Passed)
	assert.Equal(t, "fail", checkStatus(report, "binary:mysqldump"))
	assert.Equal(t, "skipped", checkStatus(report, "database:site-a:db1"))
	assert.Equal(t, "mysqldump not found", checkMessage(report, "database:site-a:db1"))
	assert.Equal(t, "skipped", checkStatus(report, "database:site-b:db1"))
	assert.Empty(t, mysqlProber.probed, "prober must not run for a missing binary")
}

func TestRunFiltersSitesAndDeduplicatesStorages(t *testing.T) {
	cfg := probeConfig(t)
	checker, mysqlProber, _, stores := probeChecker(t, cfg)

	report, err := checker.Run(context.Background(), "site-a")
	require.NoError(t, err)
	assert.True(t, report.Passed)
	assert.NotContains(t, checkNames(report), "database:site-b:db1")
	assert.NotContains(t, checkNames(report), "storage:m-storage")
	assert.NotContains(t, checkNames(report), "engine:site-b")
	assert.Contains(t, checkNames(report), "database:site-a:db2")
	assert.Contains(t, checkNames(report), "storage:z-storage")

	require.Len(t, mysqlProber.probed, 1)
	assert.Equal(t, "user-a1", mysqlProber.probed[0].Username)
	assert.Equal(t, 1, stores["a-storage"].probes)
}

func TestRunCompletesWhenAProbeIgnoresItsTimeout(t *testing.T) {
	oldTimeout := probeTimeout
	probeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { probeTimeout = oldTimeout })

	cfg := probeConfig(t)
	checker, mysqlProber, _, _ := probeChecker(t, cfg)
	mysqlProber.blockOnCtx = true

	report, err := checker.Run(context.Background(), "")
	require.NoError(t, err)
	assert.False(t, report.Passed)
	assert.Equal(t, "fail", checkStatus(report, "database:site-a:db1"))
	assert.Equal(t, "context deadline exceeded", checkMessage(report, "database:site-a:db1"))
}

func TestRunNeverLeaksSecretsIntoCheckMessages(t *testing.T) {
	cfg := probeConfig(t)
	checker, mysqlProber, _, _ := probeChecker(t, cfg)
	mysqlProber.probeErr = errors.New("authentication failed")
	checker.StoreErrs = map[string]error{"m-storage": errors.New("could not prepare a storage destination")}
	delete(checker.Stores, "m-storage")

	secrets := []string{"super-secret-db-pass", "super-secret-key", "secret-endpoint.example"}
	cfg.Sites[0].Sources.Databases[0].Password = secrets[0]
	cfg.Storages["a-storage"] = config.Storage{Type: "s3", AccessKeyID: "key", SecretAccessKey: secrets[1], Endpoint: "https://" + secrets[2]}

	report, err := checker.Run(context.Background(), "")
	require.NoError(t, err)
	for _, check := range report.Checks {
		for _, secret := range secrets {
			assert.NotContains(t, check.Message, secret)
		}
	}
}

func checkMessage(report DoctorReport, name string) string {
	for _, check := range report.Checks {
		if check.Name == name {
			return check.Message
		}
	}
	return ""
}

func checkNames(report DoctorReport) []string {
	names := make([]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		names = append(names, check.Name)
	}
	return names
}
