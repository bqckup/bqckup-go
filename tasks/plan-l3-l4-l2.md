# Implementation Plan: Pure-Go Restic Engine — L3 / L4 / L2 Roadmap

**Status:** Planned. Design review required per phase before any code.
**Order:** L3 (S3/R2 backend) → L4 (locking) → L2 (forget + prune).
**Branch:** work continues on `7-incremental-backup`; no new branch.
**Milestones:** M12 (L3), M13 (L4), M14 (L2) in `docs/intern-backlog.md`.
**Prerequisite:** L1 engine (`internal/engine/restic/`) complete — merge/review status tracked in `tasks/plan.md`.

## 1. What this plan is about (short summary)

L1 delivered the builtin pure-Go restic engine for **local** repositories:
init, backup with Rabin CDC dedup, encrypted packs/index/trees/snapshots,
minimal retention (snapshot-record deletion only), and `restic check`
compatibility. This plan maps the three deferred phases from the Phase 1 design (labels L2/L3/L4; dependency order differs from label order):

- **L3** — implement the engine `Backend` interface over S3/R2 so
  `engine: builtin` works for cloud destinations.
- **L4** — restic-compatible locking so concurrent writers cannot corrupt a
  repository.
- **L2** — forget + mark-and-sweep prune so retention actually reclaims
  pack space.

Restore remains deferred (its rules are already locked: explicit
destination, no silent overwrite).

## 2. Rules BEFORE you start (the gate)

- The restic roadmap gate (`references/restic-roadmap.md`) applies to every
  phase: a **design-only PR** must be reviewed and approved before any code,
  config, or dependency change for that phase.
- No new branches. Work on `7-incremental-backup`.
- Keep the engine CLI-only, secret-safe: credentials in memory only; nothing
  secret in logs, errors, history, fixtures, or output.
- Keep the official-binary compatibility promise: every phase must keep the
  `restic_compat` suite green (`go test -race -tags=restic_compat ...`).
- The process adapter (`internal/backup/restic/`) stays for v1 repositories
  and for sites that still choose `engine: restic`. L3 does not remove it.
- `make verify` and `sh scripts/check-docs.sh` pass at every checkpoint.

## 3. Word list (for readers who are not native English speakers)

- **Backend** — the storage layer of the engine (local disk today; S3/R2 in
  L3). A repository is a set of small objects behind this interface.
- **Layout** — how repository files map to paths or object keys. Only the
  restic `default` layout is in scope.
- **Lock** — a small marker file telling other writers "I am working here".
  **Exclusive** = only me; **non-exclusive** = readers may join.
- **Stale lock** — a lock left behind by a crashed run. Older than 30
  minutes = stale (restic's rule).
- **Forget** — delete snapshot records per retention policy.
- **Prune** — delete data (packs) that no kept snapshot references.
- **Repack** — rewrite partially-used packs to squeeze out dead blobs.
  Deferred: future option, not part of this plan.
- **Mark & sweep** — mark every blob reachable from kept snapshots, then
  delete everything unmarked.

## 4. What exists today (current state)

- L1 engine complete (`internal/engine/restic/`): crypto, chunker, local
  backend, pack, index, tree, snapshot, repository, archiver, facade.
- `Backend` interface (`internal/engine/restic/backend`) has exactly one
  implementation: `Local` (atomic staged writes, 0700/0600).
- `engine: builtin` is validated to **local** destinations only; s3/r2 sites
  must use `engine: restic` (the subprocess adapter).
- The builtin engine writes no locks and ignores restic-created locks.
- Retention (`keep_last` per site tag) deletes snapshot files only; pack
  bytes stay. `Unlock` on the builtin engine is a documented no-op.
- S3/R2 already exist for archive artifacts via `internal/storage/s3compat`
  (AWS SDK v2 + `transfermanager.UploadObject`, fake-SDK tests) — a pattern
  to reuse, not a component to extend.

## 5. Agreed decisions (from the design interview)

| # | Decision | Chosen |
| :--- | :--- | :--- |
| D1 | Phase order | L3 → L4 → L2 |
| D2 | Structure | One roadmap (this doc) + per-phase design PR + per-phase implementation milestone; no combined design for all three |
| D3 | S3 layout | restic `default` only; no `s3legacy` |
| D4 | L3 backend | New `backend/s3.go` implementing the engine `Backend` interface, reusing the s3compat SDK/transfermanager patterns; `store.go` untouched |
| D5 | L4 lock policy | Follow restic exactly: exclusive for backup/prune, non-exclusive for listing; stale non-exclusive (>30 min) auto-removed; stale exclusive → error suggesting `unlock`; `Unlock` becomes real |
| D6 | L2 prune scope | Forget + mark-and-sweep prune, **no repack** in the first iteration; repack written up as a future option with a size threshold |

## 6. Task list

### PHASE L3 — S3/R2 backend (M12)

**Status: DONE (this session).** Design decisions recorded in §5 (D3/D4);
backend contract mapping, upload strategy and credential flow implemented in
`internal/engine/restic/backend/s3.go` with fake-SDK contract tests
(`s3_test.go`). `RepoConfig` carries bucket/endpoint/prefix in memory; the
local-only gate in `internal/app` was removed; doctor no longer reports it.
MinIO/restic_compat integration is the one open item (see Q1).

#### L3-D1 — Design PR (Size S, docs only)

- Backend contract mapping: `Handle{Type, Name}` → object key under the
  `default` layout (config, keys/, data/xx/, index/, snapshots/, locks/).
- Upload strategy: `transfermanager.UploadObject` (same as s3compat).
  `Load(offset, length)` via ranged `GetObject`.
- Credential flow: values arrive in memory through `RepoConfig` (the runner
  already loads the 0600 runtime storage file); never logged.
- Config validation change: `engine: builtin` + s3/r2 destination becomes
  valid; v1-repo and process-adapter rules unchanged.
- Test strategy: fake-SDK contract tests (network-free) + optional
  disposable MinIO integration, mirroring M04.
- Gate: maintainer approval.

#### L3-T1 — Backend contract over S3 (Size M, deps: L3-D1)

Fake SDK: Save/Load (offset+length)/Stat/List(FileType)/Remove/IsNotExist.
Layout key mapping tests.

#### L3-T2 — Wire credentials + config validation (Size S, deps: L3-T1)

Extend facade `RepoConfig` with access key/secret/region (in memory);
remove the local-only gate; keep redaction through every output boundary.

#### L3-T3 — MinIO integration + restic_compat over S3 (Size M, deps: L3-T1)

Compatibility harness against a MinIO-backed repository: init, backup,
`restic check`, `restic snapshots`, `restic restore`, second-backup dedup.

### PHASE L4 — Lock management (M13)

**Status: DONE (this session).** Format verified against restic 0.16.4 and
0.19.1 sources (verification notes §2.11): lock blobs are the restic
"unpacked" format — `0x02 || zstd(JSON)`, sealed with the repository master
key, content-addressed names. Locking algorithm mirrors restic
(check → create → 200 ms wait → re-check; Refresh rewrites the lock).
`bqckup backup unlock <site>` removes stale + invalid locks (facade and
runner paths). Cross-tool compat tests pass against the official restic
0.19.1 binary in both directions (restic blocks on our exclusive lock;
our exclusive lock blocks restic's running backup).

#### L4-D1 — Design PR (Size S, docs only)

- Lock file format identical to restic (JSON: time, exclusive, hostname,
  username, pid, uid, gid) — verified against restic source in the
  verification notes.
- Semantics per D5; which bqckup operations take which lock; stale-lock
  math (30 minutes); S3 nuance: object creation is not compare-and-swap, so
  locks are random-named files + list-based detection (restic's approach).
- Gate: maintainer approval.

#### L4-T1 — Lock file format + backend writes (Size S, deps: L4-D1)

Serialize/parse restic lock JSON; save/remove via the Backend interface
(works for local and S3).

#### L4-T2 — Acquisition & staleness (Size M, deps: L4-T1)

Exclusive/non-exclusive acquisition, stale non-exclusive auto-removal,
stale exclusive → error; wire into Backup and retention entry points.

#### L4-T3 — Unlock + cross-tool tests (Size M, deps: L4-T2)

Real `Unlock` (facade + runner path); compat tests proving the official
binary blocks on our exclusive lock and we block on its.

### PHASE L2 — Forget + prune (M14)

**Status: DONE (this session).** `repository.ForgetAndPrune` (prune.go):
forget by site tag keeps the newest `keep_last` snapshots; the reachable
set is built by walking kept snapshot trees (tree DAG dedup); the sweep
writes the new index (without dead packs) first, removes the old index
files, then deletes dead and orphaned packs — crash-safe at every step.
No repack: partially-referenced packs survive whole. Reclaimed bytes flow
through `ApplyRetention` → run output (`reclaimed_bytes`, human-readable
text line too). Compat: `restic check`/`snapshots`/`restore` verified
against the official restic 0.19.1 binary after prune.

#### L2-D1 — Design PR (Size M, docs only)

- Forget: match snapshots by site tag, keep newest `keep_last` (existing
  policy semantics unchanged), delete the rest.
- Prune: reachability mark from kept snapshot trees → write the new index
  (without dead packs) FIRST, then delete the unreferenced packs — the
  inverse of the L1 write order, so a crash at any point leaves `restic
  check` green.
- Memory bounds for the reachability set on large repositories.
- Repack: documented as a future option with a size threshold — not built.
- Gate: maintainer approval.

#### L2-T1 — Forget by tag (Size S, deps: L2-D1)

Replace snapshot-file-only deletion; retention semantics stay L1-minimal
(snapshot records only) until prune ships.

#### L2-T2 — Mark phase (Size M, deps: L2-D1)

Walk kept snapshots → trees → blob IDs; build the reachable set.

#### L2-T3 — Sweep phase + index rewrite (Size M, deps: L2-T2)

Write the new index (without dead packs) and swap it in first, then delete
unreferenced packs; cancellation at any point leaves the repository
consistent (`restic check` green).

#### L2-T4 — Space reporting + compat tests (Size S, deps: L2-T3)

Report reclaimed bytes in run output; `restic check` green after prune.

## 7. Risks

- **S3 has no atomic create** (L4): a crashed writer can leave a stale lock
  that list-based detection must classify — restic's 30-minute rule is the
  accepted trade-off. Mitigated by following restic semantics exactly.
- **Prune memory** (L2): the reachable set can be large on big
  repositories. Mitigation: bounded representation designed in L2-D1; do
  not build a streaming rewrite before measuring.
- **Cross-tool lock trust** (L4): both sides must honor each other's locks
  or the protection is decorative. Mitigation: compat tests in both
  directions are acceptance criteria, not nice-to-haves.
- **Prune + crash** (L2): deleting packs while the old index still lists
  them would break `restic check` if interrupted. Mitigation: write the new
  index first, then delete packs — the inverse of the L1 invariant (backup:
  data exists before the index references it; prune: the index stops
  referencing data before the data disappears).

## 8. Open questions (owner: maintainer)

| # | Question | Where decided |
| :--- | :--- | :--- |
| Q1 | MinIO integration: required in CI, or opt-in like the compat tag? | L3-D1 |
| Q2 | Repack option: publish the threshold config now or when built? | L2-D1 — **deferred**: record repack as a future option when built, no config now |
| Q3 | Lock refresh: restic renews its locks every ~5 minutes during long operations so a >30-minute run never looks stale — confirm whether bqckup implements the same refresh loop or accepts stale-exclusive errors on long runs | L4-D1 — **resolved**: bqckup refreshes its lock every 5 minutes during backup/retention (mirrors restic) |
| Q4 | After L3: make `builtin` the default `incremental.engine`? | maintainer, post-M12 |
| Q5 | Prune reporting: log-only, or a history table column? | L2-D1 — **resolved**: run output only (`reclaimed_bytes` in JSON, human line in text); no history column |

## 9. Deferred (do NOT build in this plan)

- Repack (future L2 option with size threshold).
- Restore operations (explicit destination + no-overwrite rules locked).
- Removing the process adapter (v1 repositories still need `engine: restic`).
- `s3legacy` layout.
- Rustic compatibility. Never mention Rustic settings in this work.
