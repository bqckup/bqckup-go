# Incremental Backup via Restic Engine Specification

## Objective

Add opt-in **Incremental Backup** capability to `bqckup-go` powered by the **Restic** engine, while preserving the existing **Full Archive** (`.tar.gz`) mode as the default.

For sites with large file directory trees (e.g. multi-gigabyte WordPress uploads, media assets, static user data), full archive backups create heavy bandwidth, storage, and runtime overhead on every run. Incremental backup mode provides content-defined chunking (CDC), deduplication, and encrypted snapshots so only changed data blocks are uploaded, drastically reducing backup time and storage costs.

## Tech Stack & Commands

- **Language & Runtime:** Go 1.22+ (Strict CLI-only architecture)
- **Engine Dependency:** External `restic` binary (checked via `bqckup doctor` and invoked via `exec.CommandContext`)
- **Storage Compatibility:** Local filesystem and S3/R2-compatible storage
- **Commands:**
  ```bash
  # Verification & Quality Gate
  make verify                 # Run format, vet, race-detector unit tests, and build
  sh scripts/check-docs.sh    # Verify document link integrity and standards

  # CLI Usage
  bqckup run --site <name>    # Executes backup (full archive or incremental based on site config)
  bqckup doctor               # Preflight checks: validates configuration, permissions, and tool dependencies (including restic)
  bqckup history              # Records run status, snapshot ID/checksum, and duration
  ```

## Project Structure

```text
internal/
├── app/                      # Concrete wiring and dependency injection
├── backup/                   # Core backup orchestration
│   ├── archiver/             # Existing full archive (.tar.gz) engine
│   ├── database/             # Database exporters (MySQL, PostgreSQL, SQLite)
│   └── restic/               # Restic process adapter, repository management, snapshotting, and retention
├── cli/                      # Cobra commands (run, doctor, history, config)
├── config/                   # Schema-v2 parsing and validation (Viper/YAML)
└── history/                  # SQLite execution history tracking
docs/
├── superpowers/specs/        # Architectural design specifications
├── configuration-v2.md       # Canonical configuration documentation
└── restic-roadmap.md         # Long-term Restic integration lifecycle
```

## Configuration Contract (Schema v2)

Incremental backup is configured per-site in `site.yml` via the `backup_mode` field:

```yaml
site:
  name: wordpress-production
  # backup_mode accepts 'full' (default) or 'incremental'
  backup_mode: incremental

  # Incremental engine settings (required when backup_mode: incremental)
  incremental:
    engine: restic
    # Reference to environment variable holding repository password (never plaintext)
    password_env: RESTIC_PASSWORD

sources:
  files:
    - path: /var/www/html/wp-content/uploads
      exclude:
        - "*.tmp"
        - "cache/**"
  databases:
    - name: wp_db
      enabled: true
      engine: mysql
      host: localhost
      port: 3306
      database: wordpress
      username: wp_backup
      password: <runtime-secret>

destinations:
  - storage: local-backup
  - storage: offsite-s3
```

### Validation Rules
1. `backup_mode` must be either `full` (default if omitted) or `incremental`.
2. When `backup_mode: incremental`:
   - `incremental.engine` must be `restic`.
   - `incremental.password_env` must be a non-empty, valid environment variable name matching `^[A-Z_][A-Z0-9_]*$`.
   - Plaintext repository passwords are strictly prohibited in configuration files.
   - If the environment variable specified in `password_env` is missing or empty at runtime, the run fails immediately in preflight with a redacted error.

## Architecture & Adapter Boundary

### Consumer-Owned Interface
The runner interacts with the incremental engine via a consumer-owned interface defined in `internal/backup`:

```go
package backup

import "context"

type IncrementalEngine interface {
    // EnsureRepository initializes the repository at the destination if not already initialized.
    EnsureRepository(ctx context.Context, repo RepoConfig) error

    // BackupFiles creates a deduplicated snapshot of the source file paths.
    BackupFiles(ctx context.Context, repo RepoConfig, spec FileBackupSpec) (SnapshotResult, error)

    // ApplyRetention executes snapshot retention (forget & prune) for the site.
    ApplyRetention(ctx context.Context, repo RepoConfig, keepLast int) error
}
```

### Process Execution & Secret Redaction
- All `restic` invocations use `exec.CommandContext` with explicit argument slices. No shell execution (`sh -c`) is ever used.
- Secrets (`RESTIC_PASSWORD`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`) are passed exclusively through the child process environment slice.
- Standard output and error streams are captured into bounded buffers. If `restic` fails, error messages are sanitized and redacted to prevent leaking credentials, repository URLs, or host paths.
- Repository locations:
  - **Local:** `/var/backups/bqckup/restic/<site>`
  - **S3:** `s3:<endpoint>/<bucket>/<prefix>/restic/<site>`

## Lifecycle & Failure Semantics

1. **Preflight / Doctor:**
   - Verify `restic` binary is executable in `PATH`.
   - Verify `password_env` is set in the runtime environment.
   - Check destination reachability and write permissions.

2. **Backup Workflow:**
   - **Repository Initialization:** If the repository is not yet initialized, run `restic init`.
   - **File Snapshot:** Run `restic backup <paths> --exclude <patterns> --tag bqckup --tag site:<site>`.
   - **Databases:** Dump databases using existing secure exporters (`mysqldump`, `pg_dump`), then include dump artifacts in the snapshot or store them as verified artifacts.
   - **History Recording:** Record the snapshot ID, duration, and deduplicated byte stats in the history database.

3. **Retention & Pruning:**
   - Runs `restic forget --keep-last <keep_last> --prune --tag site:<site>`.
   - **Safety Gate:** Retention and pruning are executed **only** if the backup completed with 100% success across all sources and destinations. Any export or snapshot failure halts execution immediately without pruning previous snapshots.

4. **Context Cancellation:**
   - When context is cancelled (`SIGINT`, `SIGTERM`), the child `restic` process is immediately terminated.
   - Incomplete state or lock files are handled cleanly on the next run with `restic unlock`.

## Testing Strategy

1. **Unit & Contract Tests (Network & Binary Free):**
   - Use a `ProcessExecutor` interface to verify exact `restic` arguments, flags (`--json`, `--exclude`, `--tag`), and environment variables.
   - Validate that credentials never appear in formatted errors or log outputs.
   - Verify that invalid exit codes, lock conflicts, and uninitialized repositories trigger the correct error types.

2. **Configuration Tests:**
   - Test default fallback to `full` archive mode.
   - Test valid and invalid `backup_mode` and `incremental` settings.
   - Test detection and rejection of plaintext passwords or invalid `password_env` names.

3. **Integration Tests (Opt-in / Disposable):**
   - End-to-end testing with temporary local repositories to verify snapshot creation, file modifications, incremental deduplication, and `forget --prune` behavior.

## Boundaries

- **Always:**
  - Redact passwords and credentials from all error messages, logs, history, and CLI outputs.
  - Pass `context.Context` to all subprocesses and handle cancellation cleanly.
  - Keep `full` `.tar.gz` archive mode working as the default when incremental is not configured.
  - Require `0600` permissions on runtime config files containing secrets.
- **Ask First:**
  - Altering database export pipelines or artifact naming schemes.
  - Modifying the global SQLite history database schema.
- **Never:**
  - Accept plaintext repository passwords in YAML files.
  - Build or execute shell strings.
  - Run retention/pruning after a failed backup run.
  - Silently convert or delete unmanaged snapshots.

## Success Criteria

1. Running `bqckup run` on a site with `backup_mode: incremental` creates an encrypted, deduplicated Restic snapshot on configured local and S3 destinations.
2. Subsequent runs with minor or no file changes complete in seconds with minimal bytes uploaded.
3. Sites without `backup_mode: incremental` continue creating standard `.tar.gz` full archives without behavioral change.
4. `bqckup doctor` accurately reports whether the `restic` binary is installed and operational.
5. All secrets remain strictly in memory and child process environments, with zero leakage in errors or history.
6. `make verify` and `sh scripts/check-docs.sh` pass cleanly with 100% test coverage of new code paths.
