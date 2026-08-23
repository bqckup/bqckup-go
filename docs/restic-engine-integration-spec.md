# Incremental Engine Integration

## Runtime decision

Bqckup has one incremental runtime engine: the in-tree pure-Go implementation
under `internal/engine/restic`, wired through
`internal/engine/restic/facade`. Production code does not search for or execute
an external Restic binary.

The consumer-owned `backup.IncrementalEngine` contract covers repository
initialization, file backup, retention/prune, and stale-lock removal. The
runner receives exactly one implementation from `internal/app`; there is no
runtime engine selector or fallback adapter.

## Configuration

Incremental sites select the mode and repository-password environment
variable only:

```yaml
site:
  name: example
  enabled: true
  backup_mode: incremental
  incremental:
    password_env: RESTIC_PASSWORD
```

The removed `incremental.engine` field is rejected by strict schema-v2
decoding. Existing configurations must delete that field. Repositories must
already use Restic repository format v2; migrating format-v1 repositories is
an explicit external operation and is not performed silently by Bqckup.

## Compatibility verification

Tests tagged `restic_compat` may execute the official Restic binary as an
oracle. They verify that repositories produced by the Go engine pass official
`check`, snapshot listing, restore, and cross-tool lock tests. These opt-in CI
tests do not create a production runtime dependency.

## Security and operational rules

- Repository passwords are resolved from `incremental.password_env`, remain
  in memory, and never appear in arguments, logs, history, fixtures, or errors.
- S3/R2 credentials retain the protected mode-`0600` storage-file contract.
- Cancellation propagates through backend I/O and incomplete writes are
  cleaned up.
- Retention runs only after every required destination succeeds.
- Restore remains deferred and will require an explicit destination with
  no-overwrite behavior.
