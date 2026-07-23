# Architecture routing

The canonical source is [`docs/architecture.md`](../../../../docs/architecture.md). Read it before changing package responsibilities.

## Dependency decisions

- `internal/cli` parses and renders; it does not execute backup logic.
- `internal/app` constructs concrete adapters and owns their lifecycle.
- `internal/backup` owns orchestration and the interfaces it consumes.
- `internal/config` is the only Viper boundary.
- `internal/history` is the only GORM boundary.
- `internal/storage/<adapter>` implements the storage contract without leaking SDK types into the runner.
- Database exporters belong below `internal/backup/database` and execute external tools through an injected process boundary.

Add an abstraction only when the selected vertical slice has a real consumer. Prefer extending an existing narrow contract over creating a generic framework.

## Invariants

- One site lock, one running record, and exactly one attempted terminal update per started run.
- Every produced artifact goes to every configured destination.
- Retention begins only after all required operations succeed.
- Temporary and staging files are owner-only and removed on failure/cancellation.
- UTC object keys remain `bqckup/<site>/<timestamp>/<artifact>`.
