# MySQL and PostgreSQL Database Exporters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add secure, cancellable MySQL/MariaDB and PostgreSQL database exporters whose compressed artifacts participate in the existing multi-destination backup runner.

**Architecture:** The `internal/backup` package owns the exporter interface because it already owns `Artifact` and the runner use case. `internal/backup/database` contains concrete process-backed MySQL and PostgreSQL adapters and an injected process boundary. `internal/config` validates inline runtime passwords and protects credential-bearing site files; `internal/app` constructs and preflights exporters.

**Tech Stack:** Go 1.26, `os/exec` with `exec.CommandContext`, gzip, SHA-256, Viper/mapstructure validation, Testify, existing SQLite history and local/S3/R2 storage adapters.

## Global Constraints

- Support exactly `mysql` and `postgres` in this milestone; SQLite, repair, restore, and Restic remain deferred.
- Enabled sources require `name`, `engine`, `host`, `port`, `database`, `username`, and inline `password`.
- Any site file containing a database password must be a regular, non-symlink file with exact mode `0600`.
- Passwords are copied only to the child process environment (`MYSQL_PWD` or `PGPASSWORD`) and never appear in arguments, errors, logs, history, fixtures, examples, or Git.
- Use `exec.CommandContext` with explicit argument slices; never invoke a shell or construct a shell command string.
- Emit owner-only `.sql.gz` artifacts with SHA-256 and size metadata; delete partial artifacts on every failure or cancellation.
- Database object keys are `bqckup/<site>/<timestamp>/databases/<source-name>.sql.gz`.
- All configured destinations are required; exporter, upload, or history failure prevents retention and preserves prior successful backup sets.
- Default tests never require database binaries, network access, or real credentials; live database tests are opt-in.
- Run `make verify` and `sh scripts/check-docs.sh` before completion.

---

### Task 1: Add and validate inline database configuration

**Files:**
- Modify: `internal/config/types.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/validate.go`
- Create: `internal/config/database_validation_test.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/config/validate_test.go`

**Interfaces:**
- Consumes: `config.DatabaseSource` and the existing `Config.Load`/`Config.Validate` flow.
- Produces: `DatabaseSource.Password`, strict MySQL/PostgreSQL validation, and mode-0600 site-file protection.

- [ ] **Step 1: Write failing configuration tests**

Add table-driven tests for valid MySQL and PostgreSQL entries, unsupported `sqlite`/`mongo`, missing name/engine/host/database/username/password, invalid ports (`0`, `65536`), duplicate enabled names, and disabled incomplete entries. Add load tests proving a password-bearing site file with mode `0640` and a symlinked site file are rejected without including the password in the error.

```go
func TestValidateEnabledDatabaseSources(t *testing.T) {
    tests := []struct { name string; db DatabaseSource; wantErr string }{
        {name: "mysql", db: validDatabase("mysql")},
        {name: "postgres", db: validDatabase("postgres")},
        {name: "unsupported engine", db: validDatabase("sqlite"), wantErr: "engine"},
        {name: "missing password", db: func() DatabaseSource { db := validDatabase("mysql"); db.Password = ""; return db }(), wantErr: "password"},
        {name: "invalid port", db: func() DatabaseSource { db := validDatabase("mysql"); db.Port = 65536; return db }(), wantErr: "port"},
    }
    for _, test := range tests {
        t.Run(test.name, func(t *testing.T) {
            cfg := validConfig(t)
            cfg.Sites[0].Sources.Databases = []DatabaseSource{test.db}
            err := cfg.Validate()
            if test.wantErr == "" { require.NoError(t, err); return }
            require.Error(t, err)
            assert.Contains(t, err.Error(), test.wantErr)
            assert.NotContains(t, err.Error(), "database-secret")
        })
    }
}
```

- [ ] **Step 2: Run focused tests and verify RED**

Run: `go test ./internal/config -run 'Test(Validate.*Database|LoadRejectsDatabaseCredential)' -count=1`

Expected: FAIL because `DatabaseSource` has no inline password field, enabled databases currently return the generic unsupported-milestone error, and site-file security is not enforced.

- [ ] **Step 3: Add the typed password field and validation**

Add `Password string ` + "`mapstructure:"password" yaml:"password"`" + ` to `DatabaseSource`. Replace the current enabled-database rejection in `validateSite` with a helper that validates enabled entries and detects duplicate names. Accept exactly `mysql` and `postgres`; require non-empty host, database, username, password, and a port in `1..65535`. Do not include field values in errors.

```go
func validateDatabaseSource(file, field string, source DatabaseSource) error {
    if !source.Enabled { return nil }
    if !safeName.MatchString(source.Name) { return validationError(file, field+".name", "must be a safe source name") }
    if source.Engine != "mysql" && source.Engine != "postgres" {
        return validationError(file, field+".engine", "must be mysql or postgres")
    }
    if source.Host == "" { return validationError(file, field+".host", "is required") }
    if source.Port < 1 || source.Port > 65535 { return validationError(file, field+".port", "must be between 1 and 65535") }
    if source.Database == "" { return validationError(file, field+".database", "is required") }
    if source.Username == "" { return validationError(file, field+".username", "is required") }
    if source.Password == "" { return validationError(file, field+".password", "is required") }
    return nil
}
```

- [ ] **Step 4: Protect credential-bearing site files during Load**

After decoding each site document and before appending it, call `validateDatabaseCredentialFile(sitePath, doc.Site)`. If any database has a non-empty `Password`, use `os.Lstat`; reject symlinks, non-regular files, and any mode other than `0600`. Return a redacted validation error that names only the file and field path.

- [ ] **Step 5: Run config tests and commit**

Run: `go test ./internal/config -count=1`

Expected: PASS, including existing storage/file validation. Commit:

```bash
git add internal/config/types.go internal/config/load.go internal/config/validate.go internal/config/database_validation_test.go internal/config/load_test.go internal/config/validate_test.go
git commit -m "feat: validate inline database credentials"
```

### Task 2: Build process-backed MySQL and PostgreSQL exporters

**Files:**
- Modify: `internal/backup/types.go`
- Create: `internal/backup/database/process.go`
- Create: `internal/backup/database/exporter.go`
- Create: `internal/backup/database/exporter_test.go`

**Interfaces:**
- Consumes: `config.DatabaseSource`, `backup.Artifact`, and a `context.Context`.
- Produces: `backup.Exporter`, `database.ProcessRunner`, `database.NewProcessRunner`, `database.NewMySQL`, and `database.NewPostgres`.

- [ ] **Step 1: Define the exporter interface and failing contract tests**

Add to `internal/backup/types.go`:

```go
type Exporter interface {
    Export(ctx context.Context, source config.DatabaseSource, destination string) (Artifact, error)
}
```

Define a fake process runner that captures command, arguments, environment, stdout writer, and stderr writer. Test that MySQL uses `mysqldump` and `MYSQL_PWD`, PostgreSQL uses `pg_dump` and `PGPASSWORD`, neither password appears in arguments or errors, gzip output decompresses to fake SQL, returned metadata has `SourceKind: "database"`, and cancellation/non-zero exit removes the destination.

- [ ] **Step 2: Run exporter tests and verify RED**

Run: `go test ./internal/backup/database -run 'Test(Export|MySQL|Postgres)' -count=1`

Expected: FAIL because the process adapter and constructors do not exist.

- [ ] **Step 3: Implement the process boundary**

Use this adapter-owned boundary:

```go
type ProcessSpec struct {
    Command string
    Args    []string
    Env     []string
    Stdout  io.Writer
    Stderr  io.Writer
}

type ProcessRunner interface {
    LookPath(command string) (string, error)
    Run(ctx context.Context, spec ProcessSpec) error
}
```

The real implementation calls `exec.LookPath` and `exec.CommandContext`, sets `cmd.Env` to inherited environment plus the one password variable, assigns stdout/stderr, and never logs `ProcessSpec.Env` or stderr.

Expose these exact constructors and methods:

```go
func NewProcessRunner() ProcessRunner
func NewMySQL(process ProcessRunner) *ProcessExporter
func NewPostgres(process ProcessRunner) *ProcessExporter
func (e *ProcessExporter) Preflight() error
func (e *ProcessExporter) Export(ctx context.Context, source config.DatabaseSource, destination string) (backup.Artifact, error)
```

- [ ] **Step 4: Implement gzip/checksum export**

Create a shared process exporter with engine-specific command construction. Open the destination with `O_CREATE|O_EXCL|O_WRONLY` mode `0600`, wrap stdout in gzip, close gzip and file, then calculate size/SHA-256. On any error remove the destination and return a stable redacted error. Use arguments equivalent to:

```text
mysqldump --host=<host> --port=<port> --user=<username> --single-transaction --quick --routines --triggers <database>
pg_dump --host=<host> --port=<port> --username=<username> --format=plain --no-owner --no-privileges <database>
```

Pass only `MYSQL_PWD=<password>` or `PGPASSWORD=<password>` in the child environment. Return `SourceName` from the validated source name and lowercase SHA-256.

- [ ] **Step 5: Run exporter tests and commit**

Run: `go test ./internal/backup/database -count=1`

Expected: PASS without external binaries. Commit:

```bash
git add internal/backup/types.go internal/backup/database/process.go internal/backup/database/exporter.go internal/backup/database/exporter_test.go
git commit -m "feat: add MySQL and PostgreSQL exporters"
```

### Task 3: Integrate database artifacts into the backup runner

**Files:**
- Modify: `internal/backup/runner.go`
- Modify: `internal/backup/runner_test.go`

**Interfaces:**
- Consumes: `backup.Exporter`, enabled site database sources, and existing `storage.Store`/history interfaces.
- Produces: sequential database export, upload, history recording, and retention suppression on failure.

- [ ] **Step 1: Write failing runner tests**

Add `DatabaseExporters map[string]Exporter` to test dependencies and cover two enabled sources, keys `bqckup/example/<timestamp>/databases/<source>.sql.gz`, every destination receiving every artifact, exporter failure recording a failed database artifact without retention, destination failure stopping later uploads, and cancellation status.

- [ ] **Step 2: Run focused runner tests and verify RED**

Run: `go test ./internal/backup -run 'TestRunner.*Database' -count=1`

Expected: FAIL because the runner has no database exporter dependency or database artifact loop.

- [ ] **Step 3: Add the exporter dependency and shared artifact helper**

Add `DatabaseExporters map[string]Exporter` to `backup.Dependencies`. Extract the existing upload/history failure path into a helper accepting `Artifact` and object key, preserving failed-artifact history and redacted categories.

- [ ] **Step 4: Export enabled databases after the file archive**

For each enabled source, select `r.dependencies.DatabaseExporters[source.Engine]`, create `workspace/databases/<source.Name>.sql.gz`, call `Export`, and use `path.Join("bqckup", site.Name, timestamp, "databases", source.Name+".sql.gz")`. Send the result through the same all-destination upload/history helper as the file archive. Missing exporter selection is an internal/preflight failure and never invokes retention.

- [ ] **Step 5: Run backup tests and commit**

Run: `go test ./internal/backup -count=1`

Expected: PASS with file-only behavior unchanged and new database cases green. Commit:

```bash
git add internal/backup/runner.go internal/backup/runner_test.go
git commit -m "feat: run database exports in backups"
```

### Task 4: Wire exporters into application construction

**Files:**
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Consumes: `config.Config`, `database.NewProcessRunner`, `database.NewMySQL`, `database.NewPostgres`, and `backup.Dependencies.DatabaseExporters`.
- Produces: concrete exporter selection and binary preflight for enabled database sources.

- [ ] **Step 1: Write failing app wiring tests**

Test that enabled MySQL/PostgreSQL sources construct the corresponding exporters when fake process paths are injected, and that an enabled source fails preflight with category 3 when its binary is unavailable. Existing app tests without enabled databases must remain network-free and binary-free.

- [ ] **Step 2: Run app tests and verify RED**

Run: `go test ./internal/app -run 'Test(Open|Build).*Database' -count=1`

Expected: FAIL because `app.Open` currently wires only the file archiver.

- [ ] **Step 3: Construct and preflight enabled-engine exporters**

Add `buildDatabaseExporters(ctx context.Context, configuration config.Config, process database.ProcessRunner)` that creates one exporter per supported engine used by an enabled source and calls its binary preflight check. `Open` passes `database.NewProcessRunner()`; tests pass a fake runner. Return a categorized preflight error without exposing command paths, passwords, or stderr, then pass the map to `backup.Dependencies`.

- [ ] **Step 4: Run app, CLI, and backup tests and commit**

Run: `go test ./internal/app ./internal/cli ./internal/backup -count=1`

Expected: PASS. Commit:

```bash
git add internal/app/app.go internal/app/app_test.go
git commit -m "feat: wire database exporters"
```

### Task 5: Update examples, documentation, and repository checks

**Files:**
- Modify: `README.md`
- Modify: `docs/configuration-v2.md`
- Modify: `docs/architecture.md`
- Modify: `docs/testing.md`
- Modify: `docs/intern-backlog.md`
- Modify: `configs/sites/example.yaml`
- Create or modify: `configs/sites/example.database.yaml`
- Modify: `scripts/check-docs.sh`

**Interfaces:**
- Consumes: the validated config fields and CLI behavior from Tasks 1–4.
- Produces: English docs and secret-safe examples for MySQL/PostgreSQL exporters.

- [ ] **Step 1: Add documentation contract checks**

Require `engine: mysql`, `engine: postgres`, `password:`, `MYSQL_PWD`, `PGPASSWORD`, and `databases/<name>.sql.gz` in canonical documentation. Reject any non-placeholder value after `password:` under `configs/`; examples must not contain a real password.

- [ ] **Step 2: Update examples and docs**

Document that passwords are inline only in runtime site files with mode `0600`, show `<runtime-secret>` or disabled entries in examples, explain required binaries, and state that SQLite, repair, restore, and Restic remain deferred. Do not put a sample password in tracked files.

- [ ] **Step 3: Run docs checks and commit**

Run: `sh scripts/check-docs.sh` and the repository English-language scan. Expected: PASS with no inline secret values. Commit:

```bash
git add README.md docs configs scripts/check-docs.sh
git commit -m "docs: document database exporters"
```

### Task 6: Complete verification and hand off the PR

**Files:**
- Modify: `docs/superpowers/plans/2026-08-05-mysql-postgres-database-exporters-implementation-plan.md` only if execution tracking is committed.

**Interfaces:**
- Consumes: all implementation tasks.
- Produces: verified branch and updated PR.

- [ ] **Step 1: Run focused package tests**

Run: `go test ./internal/config ./internal/backup/database ./internal/backup ./internal/app ./internal/cli -count=1`.

- [ ] **Step 2: Run full verification**

Run these commands separately: `make verify`, `sh scripts/check-docs.sh`, `git diff --check origin/main...HEAD`, and `git status -sb`.

Expected: vet, race tests, build, docs, and whitespace checks pass; no real credentials are present.

- [ ] **Step 3: Review the diff for secret leakage**

Confirm `rg -n 'password:|MYSQL_PWD=|PGPASSWORD=' configs docs README.md` finds only field names, environment variable names, or `<runtime-secret>` text, never a password value. Confirm no command output logs `ProcessSpec.Env` or stderr bodies.

- [ ] **Step 4: Push and update the PR**

Run `git push`, then update the PR title to `feat: add MySQL and PostgreSQL database exporters`. Do not run live database tests unless a private disposable database and credentials are supplied outside the repository.
