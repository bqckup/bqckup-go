# Architecture boundaries

Read current package contracts and their tests before changing responsibilities.

## Ownership

- `internal/cli` parses commands and renders text or JSON. It does not execute
  backup logic.
- `internal/app` loads configuration, creates concrete adapters, and owns their
  lifecycle.
- `internal/backup` orchestrates runs and owns the narrow interfaces it uses.
- `internal/notify` owns SMTP, generic webhook, Discord, payload rendering, and
  route dispatch; it implements the notifier interface owned by backup.
- `internal/doctor` owns diagnostic checks. `internal/app` builds a doctor
  checker that can report per-storage construction or provider failures.
- `internal/retention` owns full-backup set retention. Incremental snapshot
  forget/prune remains inside the incremental engine.
- `internal/config` is the only Viper boundary and produces validated,
  immutable configuration.
- `internal/history` is the only GORM boundary.
- `internal/storage/<adapter>` implements storage without leaking SDK types
  into orchestration.
- `internal/backup/database` runs exporters through an injected process
  boundary.
- `internal/engine/incremental` owns the built-in incremental repository
  implementation. Its concrete facade is wired in `internal/app`.

Add an abstraction only for a real consumer. Prefer extending an existing
consumer-owned interface over creating a generic framework.

## Run invariants

- Acquire one site lock before starting work.
- Create at most one running history record and attempt one terminal update.
- Send every artifact to every configured destination.
- Apply retention only after export, snapshot, and all storage operations
  succeed.
- Record the terminal history state before sending best-effort notifications.
  Notification failures must not change run status or history.
- Keep temporary and staging files owner-only and remove incomplete output on
  error or cancellation.
- Preserve write-once full-backup keys under
  `bqckup/<server_id>/<site>/<UTC timestamp>/<artifact>`.
- Keep incremental repositories isolated under
  `bqckup/<server_id>/<site>/incremental-backup/` for each destination.
- The empty-`server_id` path is a tested compatibility fallback, not the
  preferred layout for newly initialized deployments.
- Full mode packages file archives and database dumps. Incremental mode stores
  file snapshots in the repository while database dumps remain timestamped
  packages and receive full-set retention after a successful run.

## Read and restore invariants

- `history list` reads the SQLite run ledger; `storage list` reads live remote
  objects; `backup snapshots` reads incremental snapshots for local or remote
  destinations. Do not silently substitute one source for another.
- `backup restore` restores only an incremental snapshot to an explicit target.
  Resolve snapshots by site tag, preserve no-overwrite confirmation, pass
  cancellation through, and do not create history records.
- `storage link` may expose a temporary signed URL only as the requested command
  result. Never log or persist signed URLs.
