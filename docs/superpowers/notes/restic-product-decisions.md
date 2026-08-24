# Restic Engine — Product Decision Checklist

**Date:** 2026-08-20
**Status:** Draft — part of the Phase 0 design PR
**Answers the 12 questions from** `.agents/skills/developing-bqckup-go/references/restic-roadmap.md` (roadmap gate: product decisions BEFORE the design PR).
**Scope:** the pure-Go in-tree engine (`internal/engine/restic/`), L1 only.
**Ground truth for format facts:** `restic-format-verification.md`.

Each answer is YES / NO / PLAN with the reason and the consequence for L1.
Unanswered items are listed at the bottom with an owner.

---

## 1. Archive compatibility — YES

`backup_mode: full` (`.tar.gz` archives) stays a supported mode, unchanged.
The builtin engine only serves `backup_mode: incremental`. The process adapter
remains for the cases the builtin engine cannot serve (see #4, #11).

**Consequence:** archive regression tests stay green; nothing in the archive
path is touched.

## 2. Direct-source behavior (stdin / pipes) — NO

L1 backs up filesystem paths only, taken from `sources.files.include`
(absolute paths, already validated). No stdin, pipes, or special files.
An empty include list is a config error (already enforced today).

## 3. Repository ownership and initialization — PLAN

- The runner owns initialization: `EnsureRepository` is idempotent — if the
  repository already exists, init does nothing and destroys nothing.
- Restic repositories have no owner record; ownership = possession of the
  password. A pre-existing repo opened with the wrong password fails with
  `ErrInvalidPassword` (redacted) and the run is marked failed. We never
  re-init or overwrite an existing config.
- A local repo path that exists but contains a non-restic directory is an
  error (no silent adoption).

## 4. Supported backends — L1 local only

L1 implements the `Backend` interface with a local filesystem implementation
only. S3/R2 arrive in L3.

**Consequence:** `engine: builtin` is only valid when every destination of
the site is `storage.type: local`. Sites with s3/r2 destinations keep
`engine: restic` (the process adapter) until L3. Config validation enforces
this (Phase 1).

## 5. Binary / version policy — restic >= 0.17.0

- Repositories written by the builtin engine are restic **format v2**
  (version byte 2, zstd index, degree-53 polynomial). Minimum reader:
  restic >= 0.17.0. We never write v1.
- The `restic_compat` suite requires restic >= 0.17.0. The machine's
  installed 0.16.4 is v1-only and cannot run it (install a newer binary;
  see `restic-test-strategy.md`).
- `engine: restic` (process adapter) keeps working with whatever restic the
  user has installed; its policy is unchanged.

## 6. Snapshots / history — unchanged

The history table and runner flow stay exactly as they are: one
`history.Artifact` row per destination with `ObjectKey` = snapshot ID,
`SHA256` = snapshot ID, `Size` = `DataAdded`. `SnapshotSummary` keeps its
current shape. `ListSnapshots` lives on the engine facade only — it is NOT
added to the runner interface (see the boundary design note).

## 7. Locking — L1 writes no locks

The builtin engine never creates lock files. Locks written by the real
restic binary are ignored (they are ordinary files; `restic check` does not
care). Stale-lock detection and recovery is L4.

**Note:** the runner never calls `Unlock` today; the builtin engine's
`Unlock` is a documented no-op (boundary design note).

## 8. Retention / forget / prune — minimal retention in L1, no silent skip

`policy.keep_last` is validated >= 1 (default 7), so retention runs after
every successful incremental backup. Decision: the builtin engine implements
`ApplyRetention` in L1 in minimal form — decrypt and list snapshots, keep the
newest `keep_last` matching the site tag, delete the snapshot files of the
rest. **No prune** (deleted snapshots' data blobs remain until L2). No silent
skip: retention either runs or the run is marked failed.

Full design in `restic-adapter-boundary-design.md`.

## 9. Cancellation — defined

Ctrl-C (context cancellation):
- stops every I/O loop (file read, chunk, encrypt, pack write);
- removes staged tmp files (atomic rename means no partial file can exist at
  a final path);
- the snapshot is written LAST, after packs and index are flushed, so an
  interrupted run leaves the previous snapshot set intact and valid;
- the run is recorded `cancelled` in history (existing runner semantics).

## 10. Credentials — env var only

The password is read from the environment variable named by
`incremental.password_env` (a validated env name; never an inline value).
Local repositories need no AWS keys. `RepoConfig` carries credentials in
memory only; subprocess env vars (until the process adapter is retired) are
env vars, never argv. Nothing secret lands in YAML, logs, errors, or history.

## 11. Migration — YES, must open real-restic-made v2 repos

The builtin engine MUST open and continue repositories created by the
official restic binary in format v2 (restic >= 0.17.0) — otherwise switching
`engine: restic` → `engine: builtin` destroys backup continuity.

**Consequence (answers Q4):** L1 `MasterIndex` must LOAD existing index
files at open (decrypt, zstd-decode, parse the index JSON, populate the
in-memory index) so dedup works across the boundary.

v1 repositories (made by restic 0.16.x) are NOT supported by the builtin
engine. Users on v1 keep `engine: restic` until they migrate with the
official binary. Documented in P1-T19 migration notes.

## 12. Restore — deferred with future rules locked

Restore is not in this plan. When it is built: explicit destination is
required, and existing files are never overwritten silently (repo rule +
restic `restore --target` semantics).

---

## Unanswered items (owner: maintainer)

| # | Question | Where decided |
| :--- | :--- | :--- |
| Q1 | Confirm in-tree overrides the old separate-library decision | plan §8 |
| Q2 | Retention in L1: minimal retention (recommended) vs other options | `restic-adapter-boundary-design.md` |
| Q3 | Engine selection: `engine: builtin` config value (recommended) | `restic-adapter-boundary-design.md` |
| Q5 | Is `restic check` with restic >= 0.17.0 a hard release gate? | plan §8 |
| Q6 | Old s3/r2 repos: process adapter forever until L3? (recommended: yes) | plan §8 |
| Q7 | Compression: always on for data blobs (v2 default, recommended) | plan §8 |
