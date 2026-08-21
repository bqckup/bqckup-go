# Architecture

## Shape

Bqckup Go is a CLI-only modular monolith. One process loads immutable configuration, wires concrete adapters, executes one command, and closes its SQLite connection. There is no HTTP server, internal scheduler, or web UI.

```text
cmd/bqckup       process signals and exit
    ↓
internal/cli     Cobra parsing and text/JSON rendering
    ↓
internal/app     concrete dependency construction and lifecycle
    ↓
internal/backup  use-case orchestration and consumer-owned interfaces
    ↓
config | files | storage | history | lock
```

Dependencies point toward the use case. `backup.Runner` knows interfaces and domain values; it does not know Cobra, Viper, GORM, or a particular storage implementation. Viper is confined to `internal/config`, and GORM is confined to `internal/history`.

## Backup lifecycle

1. Load schema-v2 root/site YAML plus the unversioned storage document.
2. Resolve the requested site and concrete local, S3, or R2 destinations.
3. Acquire a non-blocking cross-process lock for the site.
4. Skip when the last success is inside `minimum_interval`, unless forced.
5. Insert a `running` history record.
6. `backup_mode: full` creates an owner-only temporary workspace and
   `.tar.gz` file archive; `backup_mode: incremental` runs the selected
   incremental engine (see below).
7. Calculate SHA-256 and size for the file archive and each enabled database export.
8. Store every artifact to every destination without overwriting; local uses atomic staging, while S3/R2 uses conditional transfer and metadata verification.
9. Record each stored artifact.
10. Apply retention after every required destination succeeds.
11. Mark the run `success`; failures and cancellation get a terminal status with a redacted message.
12. Remove temporary files and release the lock.

Multiple destinations have all-required semantics. A destination failure fails the run and prevents retention. Previously successful backup sets are not removed after a failed current run.

## Incremental engines

`backup_mode: incremental` selects its engine via `incremental.engine`:

- **`restic`** (default): runs the system `restic` binary through
  `internal/backup/restic` (the process adapter). Required for
  repositories in the old format v1 until the user migrates.
- **`builtin`**: the in-process pure-Go engine in `internal/engine/restic/`
  (facade in `internal/engine/restic/facade/`). No `restic` binary needed.
  Serves local and S3/R2 destinations (backend in
  `internal/engine/restic/backend/`: `Local` and `S3`). Writes
  Restic repository format v2: repositories made by the builtin engine pass
  the official `restic check`/`snapshots`/`restore` (restic >= 0.17.0), and
  the engine opens and continues repositories created by the real restic
  binary in format v2.

Retention (keep_last per site tag) forgets old snapshots and prunes
unreachable pack data with a mark-and-sweep pass (no repack): the new
index is written before any pack is deleted, so a crash at any point
leaves `restic check` green. Restore is a future phase with an explicit
destination and no silent overwrites.

## Boundaries

- `internal/config`: one Viper instance per YAML file, exact typed unmarshalling, defaults, validation.
- `internal/backup`: orchestration, file artifact domain types, status decisions.
- `internal/backup/files`: tar/gzip filesystem adapter with explicit symlink behavior.
- `internal/backup/database`: MySQL/PostgreSQL process exporters producing gzip-compressed, checksummed SQL artifacts.
- `internal/engine/restic`: pure-Go Restic repository format v2 implementation (crypto, chunker, backend, pack, index, tree, snapshot, repository, archiver) and its facade. Zero `github.com/restic/*` imports.
- `internal/storage`: adapter contract and portable object-key types.
- `internal/storage/local`: path-safe, checksum-verified local writes and backup-set listing.
- `internal/storage/s3compat`: shared S3/R2 verified uploads and prefix-scoped retention.
- `internal/history`: GORM models, SQLite lifecycle, ordered recorded migrations, repository queries.
- `internal/platform/lock`: Linux `flock` implementation (site-level
  mutual exclusion).
- Repository-level locking for the builtin engine lives in
  `internal/engine/restic/lock` (restic-compatible lock files: encrypted
  blobs in `locks/`, 30-minute staleness, exclusive for backup/retention,
  refresh every 5 minutes during long operations). `bqckup backup unlock
  <site>` removes stale locks.
- `internal/cli`: command parsing, presentation, and the single exit-code mapper.
- `internal/app`: the only normal place that constructs concrete dependencies.

Interfaces belong to their consumer. Do not create interfaces merely to mirror every concrete type, and do not add a public `pkg/` tree without a real external consumer.

## Persistence

SQLite runs with WAL, foreign keys, a five-second busy timeout, and one open connection. Migration version 1 creates `backup_runs` and `artifacts`; its application is recorded in `schema_migrations`. Database and parent paths are created with owner-only permissions.

Artifact keys use:

```text
bqckup/<site>/<UTC timestamp>/<artifact name>
```

The timestamp layout is `2006-01-02T15-04-05.000000000Z` (nanosecond
resolution, so two runs in the same second — a forced rerun, cron and a
manual run overlapping — never collide on the same object key; stores are
write-once). Names come from validated configuration, not raw runtime
input.

## Security rules

- Never commit runtime credentials or expose them in logs, history, arguments, errors, or fixtures; credential-bearing storage YAML must be a non-symlink `0600` file.
- Pass subprocess arguments as slices with `exec.CommandContext`; never invoke a shell.
- Keep Viper and GORM at their boundaries.
- Use restrictive file permissions and remove incomplete artifacts.
- Reject absolute, backslash, dot-segment, and escaping storage keys.
- Preserve causal errors for tests while presenting only categorized, redacted messages.
- A public contract change must update tests, examples, and canonical documentation in the same pull request.

## Deliberate exclusions

The foundation has no web UI, auth, notifications, reporting, master API, webhook, restore, internal scheduler, or Rustic. Restic integration is delivered by two engines: the process adapter (`engine: restic`) and the in-tree pure-Go engine (`engine: builtin`, local + S3/R2 storage, retention without prune until L2, no restore).
