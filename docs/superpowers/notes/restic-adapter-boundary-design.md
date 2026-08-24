# Restic Engine — Adapter Boundary, Retention/Unlock, Engine Selection

**Date:** 2026-08-20
**Status:** Draft — part of the Phase 0 design PR; an addendum to
`docs/superpowers/specs/2026-08-20-restic-engine-phase1-design.md`
**Decides:** plan P0-T5, product decisions #6, #7, #8, #11 and open
questions Q2, Q3, Q8 (recommendations — maintainer approves via the PR).

---

## 1. Facts this design starts from (current code, verified)

- The runner depends on `backup.IncrementalEngine` (4 methods:
  `EnsureRepository`, `BackupFiles`, `ApplyRetention`, `Unlock`) declared in
  `internal/backup/types.go`; the concrete type shape lives in
  `internal/backup/restic/types.go`.
- The runner CALLS three of them per incremental run:
  `EnsureRepository` → `BackupFiles` → `ApplyRetention` (after every
  destination, on success). `Unlock` is declared but NEVER called.
- `policy.keep_last` is validated `>= 1` (default 7) — retention always
  applies after a successful incremental backup.
- Config validation today: `incremental.engine` must be `"restic"`.
- Doctor today: `binary:restic` check (restic in PATH).

## 2. Problem A — interface mismatch (`ApplyRetention`/`Unlock` are L2/L4)

Options from the plan:

| Option | Shape | Cost |
| :--- | :--- | :--- |
| (a) | Builtin engine implements all 4 existing methods; retention gets a minimal L1 form, Unlock is a documented no-op | L1 scope grows slightly (spec §10 already permits "unless Q2 forces a minimal form") |
| (b) | Retention/unlock keep shelling out to the restic binary | Re-adds the external binary requirement — breaks the single-binary promise the spec exists to deliver |
| (c) | Split the interface (`IncrementalEngine` + optional `RetentionEngine`) | Changes a public internal interface — spec §8 "Ask First"; more runner churn, no functional gain |

**Recommendation: (a).**

- The builtin engine implements the existing `backup.IncrementalEngine`
  interface unchanged. No interface change, no runner change, no "Ask
  First" needed. Smallest diff that keeps every promise.
- `ApplyRetention` — minimal honest L1 form:
  1. Decrypt and parse every snapshot file in the repo (the engine has the
     master key; snapshot JSON is documented in the verification notes).
  2. Filter to the site's tag (`site:<name>`, the tag the runner already
     sets).
  3. Sort by snapshot time, newest first; delete the snapshot FILES of
     everything beyond `keep_last` (backend `Remove`).
  4. No prune: data blobs of deleted snapshots remain referenced-less until
     L2. This is stated in the output/docs, not silent.
- `Unlock` — documented no-op returning nil. The runner never calls it; the
  engine writes no locks; stale-lock recovery is L4. No silent
  misbehavior: the method exists only for interface compatibility and is
  documented as such.
- `ListSnapshots` (spec facade method): lives on the facade only. It is NOT
  added to `backup.IncrementalEngine` (answers Q8). Internally it is the
  engine-side primitive `ApplyRetention` reuses.

Runner `keep_last` flow in L1 (exact): after a successful `BackupFiles` for
each destination → `ApplyRetention(ctx, repo, keepLast, site.Name)` → on
error the run is marked failed with `CategoryStorage` (existing runner
behavior — the backup data itself stays; nothing of a prior successful set
is deleted).

## 3. Problem B — engine selection and doctor

Options from the plan:

| Option | Shape | Verdict |
| :--- | :--- | :--- |
| New config value | `incremental.engine: builtin` alongside `restic` | **Recommended** |
| Automatic by storage | local → builtin, s3/r2 → process | Rejected: conflates "where" with "which implementation"; a user with a local repo may still prefer the official binary |
| Replace the adapter | process adapter removed | Rejected: breaks s3/r2 sites (L3) and v1-repo users (migration #11) |

**Recommendation:**

- New value `incremental.engine: builtin`; valid values become
  `restic` (process adapter, existing behavior, stays the DEFAULT) and
  `builtin`. Default unchanged → zero behavior change for existing users;
  builtin is opt-in until battle-tested. This is a Phase 1 change (config
  validation + docs in P1-T17), not part of the design PR.
- The process adapter is NOT removed. It remains the engine for: s3/r2
  destinations (until L3), v1 repositories (until the user migrates), and
  anyone who prefers the official binary.
- Validation (Phase 1): `engine: builtin` + any destination whose storage
  type is not `local` = config error until L3 ships. `engine: builtin` +
  local = OK.
- Doctor per engine (Phase 1, replaces the single `binary:restic` check):
  - `engine: restic` → today's check: restic binary in PATH (ok/fail).
  - `engine: builtin` → no binary check. Checks: destination storage is
    local, `password_env` is set and the variable exists, repo directory is
    writable/creatable. All redacted.

## 4. What this changes in the plan

- P1-T17 acceptance criteria already assume this design (`engine: builtin`
  works without restic in PATH; doctor passes without the binary).
- P1-T12 (MasterIndex) must include loading existing index files at open
  (migration #11 / Q4 = YES) — one additional acceptance line there.
- L2 note: when prune ships, the minimal retention in §2 becomes
  forget+prune with the same interface method signature — no runner change.
