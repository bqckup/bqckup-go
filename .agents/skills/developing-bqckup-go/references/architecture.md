# Architecture boundaries

Read current package contracts and their tests before changing responsibilities.

## Ownership

- `internal/cli` parses commands and renders text or JSON. It does not execute
  backup logic.
- `internal/app` loads configuration, creates concrete adapters, and owns their
  lifecycle.
- `internal/backup` orchestrates runs and owns the narrow interfaces it uses.
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
- Keep temporary and staging files owner-only and remove incomplete output on
  error or cancellation.
- Preserve write-once full-backup keys under
  `bqckup/<site>/<UTC timestamp>/<artifact>`.
- Keep incremental repositories isolated under `restic/<site>/` for each
  destination.
