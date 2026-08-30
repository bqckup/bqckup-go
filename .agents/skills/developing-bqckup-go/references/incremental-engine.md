# Built-in incremental engine

Incremental mode is production behavior, not a future design cycle. The only
runtime engine is the in-tree Restic-compatible implementation under
`internal/engine/incremental`.

Before changing it, inspect the facade, repository, backend, lock, prune,
compatibility, and runner tests relevant to the request.

## Contracts

- `backup_mode: incremental` requires `incremental.password`; there is no
  engine selector.
- Read the repository password directly from protected site YAML and keep it
  in memory. Never log it or persist it outside that file.
- Support local, S3, and R2 backends through the same repository semantics.
- Repository paths are
  `<local directory>/bqckup/<server_id>/<site>/incremental-backup` or the
  equivalent object prefix. Preserve the tested `restic/<site>` fallback only
  for configurations where `server_id` is empty.
- Preserve compatibility with the official Restic repository format. Treat
  `restic_compat` tests as the executable interoperability contract.
- Backup and retention use exclusive locks. Listing uses non-exclusive locks.
  Automatically clean stale non-exclusive locks; stale exclusive locks require
  explicit `bqckup backup unlock <site>`.
- Retention keeps the newest snapshots for the site and reclaims unreachable
  data only after a successful run. Cancellation must leave a valid previous
  repository state.
- Incremental runs may also produce timestamped database dump packages. Apply
  their full-set retention only after the snapshot and every destination
  operation succeeds.
- `backup snapshots` lists live snapshots for an explicit destination.
  `backup restore` resolves `latest` or an ID prefix within the site's tagged
  snapshots, writes only to an explicit target, and honors safe overwrite
  confirmation. Restore does not write backup history.
- Full archive mode remains supported and behaviorally separate.

## Scope boundaries

Do not add Rustic, an external Restic backup subprocess, or another engine
selector. The official Restic CLI is allowed only in compatibility tests and
manual interoperability documentation, never as a Bqckup runtime dependency.
Those compatibility tests may set `RESTIC_PASSWORD` because it is the external
CLI's interface; Bqckup configuration itself always reads the literal password
from protected site YAML.
