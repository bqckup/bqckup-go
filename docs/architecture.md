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
   `.tar.gz` file archive; `backup_mode: incremental` runs the built-in
   pure-Go incremental engine (see below).
7. Calculate SHA-256 and size for the file archive and each enabled database export.
8. Store every package to every destination without overwriting; local uses atomic staging, while S3/R2 uses conditional transfer and metadata verification.
9. Record each stored package.
10. Apply retention after every required destination succeeds.
11. Mark the run `success` (or `no_change` when full-mode prepared packages match the previous successful run byte-for-byte); failures and cancellation get a terminal status with a redacted message.
12. Notify the terminal status through the configured notification routes
    (best effort; a failing channel warns and never changes the run result
    or history). Only runs recorded in history notify: skipped runs and
    preflight failures are silent.
13. Remove temporary files and release the lock.

Multiple destinations have all-required semantics. A destination failure fails the run and prevents retention. Previously successful backup sets are not removed after a failed current run.

## Incremental engine

`backup_mode: incremental` always uses the in-process pure-Go engine in
`internal/engine/incremental/` (facade in `internal/engine/incremental/facade/`). No
external Restic binary is used at runtime. The engine serves local and S3/R2
destinations and writes Restic repository format v2. Compatibility tests use
the official Restic binary as a test oracle for `check`, `snapshots`, restore,
and cross-tool locking; the binary is not part of production execution.

Repositories in Restic format v1 are not supported. They must be migrated to
format v2 before being configured in Bqckup.

Retention (keep_last per site tag) forgets old snapshots and prunes
unreachable pack data with a mark-and-sweep pass (no repack): the new
index is written before any pack is deleted, so a crash at any point
leaves `restic check` green. Set retention also prunes the
`bqckup/<server_id>/<site>/<timestamp>/` package sets in every mode, so database
dumps stored there by incremental runs are kept for `keep_last` runs
too. Restore is a future phase with an explicit destination and no silent
overwrites.

## Boundaries

- `internal/config`: one Viper instance per YAML file, exact typed unmarshalling, defaults, validation.
- `internal/backup`: orchestration, file package domain types, status decisions.
- `internal/backup/files`: tar/gzip filesystem adapter with explicit symlink behavior.
- `internal/backup/database`: MySQL/PostgreSQL process exporters producing gzip-compressed, checksummed SQL packages.
- `internal/engine/incremental`: pure-Go incremental repository format v2 implementation (crypto, chunker, backend, pack, index, tree, snapshot, repository, archiver) and its facade. Zero `github.com/restic/*` imports.
- `internal/storage`: adapter contract and portable object-key types.
- `internal/storage/local`: path-safe, checksum-verified local writes and backup-set listing.
- `internal/storage/s3compat`: shared S3/R2 verified uploads and prefix-scoped retention.
- `internal/storage/remoteconfig`: bounded HTTPS retrieval and strict decoding
  of S3/R2 configuration into process memory before adapters are constructed.
- `internal/history`: GORM models, SQLite lifecycle, ordered recorded migrations, repository queries.
- `internal/notify`: best-effort delivery of terminal run notifications over
  SMTP, generic webhooks, and Discord webhooks; shared sanitized payload,
  route dispatch, per-channel renders. Implements the consumer-owned
  `backup.Notifier` interface; no secrets in payloads or errors.
- `internal/platform/lock`: Linux `flock` implementation (site-level
  mutual exclusion).
- Repository-level locking for the builtin engine lives in
  `internal/engine/incremental/lock` (Restic-compatible lock files: encrypted
  blobs in `locks/`, 30-minute staleness, exclusive for backup/retention,
  refresh every 5 minutes during long operations). `bqckup backup unlock
  <site>` removes stale locks.
- `internal/cli`: command parsing, presentation, and the single exit-code mapper (0: success, 1: internal error, 2: configuration error, 3: preflight error, 4: execution/storage/cancellation error). A `no_change` backup is informational and exits 0.
- `internal/app`: the only normal place that constructs concrete dependencies.

Interfaces belong to their consumer. Do not create interfaces merely to mirror every concrete type, and do not add a public `pkg/` tree without a real external consumer.

## Persistence

SQLite runs with WAL, foreign keys, a five-second busy timeout, and one open connection. An idempotent AutoMigrate creates `backup_runs` and the packages table, kept under its legacy name `artifacts`; a versioned migration system will be added when schema changes need it. Database and parent paths are created with owner-only permissions.

Package keys use:

```text
bqckup/<server_id>/<site>/<DD-MMMM-YYYY>/<HH-mm-ss>/<package name>
```

The date and run directories use UTC, with English month names. Run names
carry nanosecond resolution so two runs of one site within the same UTC
second get distinct backup sets; stores are write-once, and a same-second
second run must not fail on its own first write. Listing and retention also
recognize the previous readable second-resolution layout and the flat
`2006-01-02T15-04-05.000000000Z` backup-set directory so existing archives
remain manageable. Names come from validated configuration, not raw runtime
input.

## Security rules

- Never commit runtime credentials or expose them in logs, history, arguments, errors, or fixtures; credential-bearing storage YAML must be a non-symlink `0600` file.
- Pass subprocess arguments as slices with `exec.CommandContext`; never invoke a shell.
- Keep Viper and GORM at their boundaries.
- Use restrictive file permissions and remove incomplete packages.
- Reject absolute, backslash, dot-segment, and escaping storage keys.
- Preserve causal errors for tests while presenting only categorized, redacted messages.
- A public contract change must update tests, examples, and canonical documentation in the same pull request.

## Deliberate exclusions

The foundation has no web UI, auth, reporting, master API, restore, internal scheduler, or Rustic. Incremental backup is delivered only by the in-tree pure-Go engine for local and S3/R2 storage. The external Restic binary is limited to opt-in compatibility tests.
