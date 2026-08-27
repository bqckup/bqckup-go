# Built-in incremental engine

Incremental mode is production behavior, not a future design cycle. The only
runtime engine is the in-tree Restic-compatible implementation under
`internal/engine/restic`.

Before changing it, inspect the facade, repository, backend, lock, prune,
compatibility, and runner tests relevant to the request.

## Contracts

- `backup_mode: incremental` requires `incremental.password_env`; there is no
  engine selector.
- Read the repository password from the named environment variable and keep it
  in memory. Never persist or log it.
- Support local, S3, and R2 backends through the same repository semantics.
- Repository paths are `<local directory>/restic/<site>` or the equivalent
  `<prefix>/restic/<site>` object prefix.
- Preserve compatibility with the official Restic repository format. Treat
  `restic_compat` tests as the executable interoperability contract.
- Backup and retention use exclusive locks. Listing uses non-exclusive locks.
  Automatically clean stale non-exclusive locks; stale exclusive locks require
  explicit `bqckup backup unlock <site>`.
- Retention keeps the newest snapshots for the site and reclaims unreachable
  data only after a successful run. Cancellation must leave a valid previous
  repository state.
- Full archive mode remains supported and behaviorally separate.

## Scope boundaries

Do not add Rustic, an external Restic backup subprocess, or another engine
selector. Bqckup has no restore command; changes to restore behavior require an
explicit product request and must use an explicit destination with safe
no-overwrite defaults. The official Restic CLI may be documented as an
interoperability and manual-restore tool, not as a Bqckup runtime dependency.
