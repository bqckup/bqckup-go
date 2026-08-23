# Pure-Go Incremental Engine Decision Record

## Decision

The transitional process adapter has been removed. Bqckup now ships one
incremental engine, compiled into the CLI as pure Go. `backup_mode: full`
remains the archive workflow; `backup_mode: incremental` always uses the
in-tree Restic-format-v2 implementation.

## Why

Keeping both a subprocess adapter and a rewritten Go engine duplicated engine
selection, preflight, error handling, retention, locking, documentation, and
tests. The completed Go engine already supports local and S3/R2 repositories,
locking, and mark-and-sweep prune, so a permanent runtime fallback no longer
justifies that surface area.

## Compatibility boundary

- Runtime: no external Restic executable.
- Tests: the official Restic executable remains an opt-in compatibility oracle.
- Repository format: v2 only. Format-v1 migration is explicit and external.
- Configuration: `incremental.engine` is removed and rejected by strict decode.

## Deferred

- Safe restore command with explicit destination and no silent overwrite.
- An in-product format-v1 migration workflow.
- Repack of partially referenced packs.
