# TODO — Pure-Go Restic Engine (Phase 1 / L1)

Full explanations live in `tasks/plan.md`. This file is only the checklist.
Read the plan first, then work top to bottom.

**HARD GATE:** Phase 0 = documents only, NO code. Phase 1 is blocked until the
maintainer approves the design PR (P0-T7). Do not edit `go.mod`, packages,
commands, or config fields before approval.

Verify command for docs-only tasks: `sh scripts/check-docs.sh`
Verify command for code tasks: `go test -race -v ./internal/engine/restic/...`
Final gate: `make verify` AND `sh scripts/check-docs.sh`

---

## PHASE 0 — Design review (start now)

### [x] P0-T1 — Reconcile spec with old planning docs (Size M, deps: none) — DONE
- [x] All three old docs got a "Superseded (2026-08-20)" banner with a link to the new spec; still-valid parts named per doc.
- [x] No doc claims both "separate library" and "in-tree" (each banner states which parts stay valid).
- [x] `docs/intern-backlog.md` M11 now points at the design artifacts (spec + notes + plan).
- [x] Only markdown files changed.
- Verify: `sh scripts/check-docs.sh` (run at design-PR time)

### [x] P0-T2 — Verify format details against official restic format (Size M, deps: none) — DONE
Verified against official restic source `a80be14` (0.19.1-dev) + chunker module v0.5.0.
Notes: `docs/superpowers/notes/restic-format-verification.md`. Spec corrected. Verdicts:
- [x] Pack header lengths — spec RIGHT (fixed 4-byte LE). uvarint doubt WRONG. No change.
- [x] Polynomial degree — spec WRONG (64-bit). Actually degree-53. Spec fixed.
- [x] Index layout — spec RIGHT (0x02 + zstd, no trailer). `supersedes` removed from example. Spec fixed.
- [x] Poly1305 — MAC formula RIGHT; `r` stored UNMASKED (clamp inside x/crypto). Spec fixed.
- [x] Key file name = SHA-256 of encrypted bytes. Confirmed.
- [x] Config JSON v2 (version/id/chunker_polynomial, new repos v2). Confirmed.
- [x] Tree JSON ({"nodes":[...]} + newline, strict sort, node types). Confirmed.
- [x] Snapshot JSON fields. Confirmed.
- [x] Notes doc written with source citations.
- [x] Spec corrected everywhere it disagreed.
- Verify: `sh scripts/check-docs.sh` (run at design-PR time)

### [x] P0-T3 — Product decision checklist (Size S, deps: none, parallel) — DONE
`docs/superpowers/notes/restic-product-decisions.md` answers all 12:
- [x] Archive mode stays supported (YES).
- [x] Direct-source: NO stdin/pipes; files only from `sources.files.include`.
- [x] Repo ownership: idempotent init, ownership = password possession, no re-init/overwrite.
- [x] Backends: L1 local-only; `engine: builtin` requires `storage.type: local` (s3/r2 keeps process adapter until L3).
- [x] Version policy: format v2; minimum reader restic >= 0.17.0 (0.16.4 is v1-only, cannot run compat suite).
- [x] Snapshots/history: unchanged (same artifact rows, same summary shape).
- [x] Locking: L1 writes no locks; restic-made locks ignored; stale-lock handling is L4.
- [x] Retention in L1: minimal form per P0-T5 note — no silent skip.
- [x] Cancellation: stop I/O, remove tmp files, snapshot written last, run recorded cancelled.
- [x] Credentials: env var only (`password_env`), never inline; no AWS keys for local.
- [x] Migration: YES — must open real-restic-made v2 repos → MasterIndex loads existing index files in L1 (answers Q4). v1 repos keep `engine: restic`.
- [x] Restore deferred; future rules locked (explicit dest, no silent overwrite).
- [x] Unanswered items listed with owner (maintainer): Q1, Q2, Q3, Q5, Q6, Q7.
- Verify: `sh scripts/check-docs.sh` (run at design-PR time)

### [x] P0-T4 — Threat model (Size S, deps: none, parallel) — DONE
`docs/superpowers/notes/restic-threat-model.md`. Asset table: asset / where it lives / who can read it / leak impact / mitigation.
- [x] User password: env var, memory only, zeroed after use, never logged.
- [x] Master key: memory only, never plaintext on disk, zeroed on close.
- [x] Key file: encrypted on disk, 0600; repo dir 0700.
- [x] Errors: RedactedError pattern; no secrets in messages.
- [x] Subprocess env (until retired): env vars only, never argv.
- [x] Spec §1.6 invariant matches the document.
- Verify: `sh scripts/check-docs.sh` (run at design-PR time)

### [x] P0-T5 — Adapter boundary + retention/unlock + engine selection design (Size M, deps: P0-T3) — DONE
`docs/superpowers/notes/restic-adapter-boundary-design.md` (spec addendum):
- [x] Problem A decided: option (a) — builtin engine implements all 4 existing `IncrementalEngine` methods, no interface change. Reasons written.
- [x] `keep_last` in L1 defined exactly: minimal retention = delete snapshot files beyond keep_last for the site tag; NO prune (L2); no silent skip.
- [x] `Unlock` = documented no-op (runner never calls it; engine writes no locks; L4 concern).
- [x] Problem B decided: new config value `engine: builtin` alongside `restic` (default unchanged); rejected auto-by-storage and replace-adapter, with reasons.
- [x] Doctor preflight per engine written down (builtin: no binary check).
- [x] Q8 answered: `ListSnapshots` lives on the facade only, not on the runner interface.
- [x] Written into a spec addendum; plan consequence noted (P1-T12 loads existing indexes).
- Verify: `sh scripts/check-docs.sh` (run at design-PR time)

### [x] P0-T6 — Test strategy document (Size S, deps: P0-T2) — DONE
`docs/superpowers/notes/restic-test-strategy.md`:
- [x] Unit tests listed per package (crypto, chunker, pack, tree, index, backend, master index).
- [x] Contract tests listed (backend shared suite; index↔pack mapping resolves to real blobs).
- [x] Round-trip tests listed (init → backup → list; dedup on 2nd run; 1-byte change; cancellation).
- [x] `restic_compat` tag flow: our engine → `restic check` (exit 0) → `restic snapshots` → `restic restore` → `diff -r` byte match.
- [x] Skip (not fail) rule written down: no binary → skip; binary < 0.17.0 → skip; >= 0.17.0 → failures are real.
- [x] CI plan: existing `verify` job unchanged; new `restic-compat` job downloads pinned restic >= 0.17.0 tarball and runs the tagged tests.
- Verify: `sh scripts/check-docs.sh` (run at design-PR time)

### [x] P0-T7 — Design PR + maintainer approval (Size M, deps: P0-T1..T6) — DONE
- [x] One design-only PR: spec + all notes + milestone split → **PR #12** (`8-restic-engine-design` → `7-incremental-backup`, docs only).
- [x] PR contains only markdown; `make verify` unaffected.
- [x] References M11 in the backlog.
- [x] Approval: the repo owner directed the agent to proceed without waiting (2026-08-20 session) — gate waived by the maintainer.
- [x] Approved plan lives in `tasks/plan.md` + `tasks/todo.md` (copied into the repo).
- Verify: `sh scripts/check-docs.sh` on the PR branch — green.

---

## GATE CHECKPOINT — Phase 1 unlocked by maintainer direction (2026-08-20)

- [x] P0-T1 … P0-T7 done.
- [x] Design PR approved — user (repo owner/maintainer) waived the wait: "don't ask me about approval just do it".
- [x] Archive mode still supported; the engine only serves `backup_mode: incremental` (regression suite green).

---

## PHASE 1 — Build the engine (BLOCKED until gate)

### [x] P1-T8 — Foundation: types, errors, crypto (Size M) — DONE
Files: `internal/engine/restic/{doc.go,types.go,errors.go}`, `crypto/{key.go,cipher.go,key_file.go,cipher_test.go}`, `go.mod`, `go.sum`
- [x] Encrypt→Decrypt round trip: empty, 1 byte, 1 MiB.
- [x] Flipped ciphertext byte → decrypt fails (MAC works).
- [x] Wrong password → ErrInvalidPassword, no secret in message.
- [x] Key file JSON matches P0-T2 format notes; name = SHA-256 of bytes.
- [x] scrypt params read FROM the key file; writer uses fixed N=65536 r=8 p=1; 64B = 32 AES + 16 k + 16 r.
- [x] REAL interop: fixture key file from restic 0.16.4 decrypts with our reader (testdata/restic-0164.key.json).
- Verify: `go test -race -v ./internal/engine/restic/...` + `make verify` green.

### [x] P1-T9 — Rabin CDC chunker (Size M) — DONE
Files: `chunker/{chunker.go,polynomials.go,chunker_test.go}` (port of restic/chunker v0.5.0, attributed)
- [x] Deterministic boundaries (same input → same chunks).
- [x] No chunk < 512 KiB or > 8 MiB (except last).
- [x] Average ≈ 1.5 MiB (theory: MinSize + 2^20; test band [1MiB, 2MiB]).
- [x] 1-byte insert mid-file changes ≤ 3 chunks.
- [x] Polynomial: degree-53 irreducible (Ben-Or test), hex JSON for the repo config.

### [x] P1-T10 — Backend: interface + local filesystem (Size M) — DONE
Files: `backend/{backend.go,layout.go,local.go,local_test.go}`
- [x] Save/Load round trip for every FileType (incl. config at repo root).
- [x] Stat/List/Remove per interface (List fixed for data/xx nesting).
- [x] Crash before rename (reader error mid-save) → no partial file at final path, tmp clean.
- [x] Dirs 0700, files 0600 (checked with os.Stat); CreateLayout makes all 256 data dirs.
- [x] Cancellation mid-Save removes tmp file.

### [x] P1-T11 — Pack builder + parser (Size M) — DONE
Files: `pack/{pack.go,builder.go,parser.go,pack_test.go}`
- [x] Build N blobs → parse → same offsets/lengths/IDs (+ payload decrypts back).
- [x] Header entries: fixed 4-byte LE lengths (verified — NOT uvarints); types 0/1/2/3.
- [x] Header length trailer written and validated.
- [x] Corrupted pack/header → clear error; empty pack rejected (build + parse).

### [x] P1-T12 — Index: persisted format + MasterIndex (Size M) — DONE
Files: `index/{index.go,master_index.go,encoder.go,index_test.go}`
- [x] Encode→Decode round trip exact (JSON spelling normalizes data/tree; compression = uncompressed_length).
- [x] Lookup returns right pack/offset/length for 10k entries.
- [x] Goroutine storm test passes under `-race`.
- [x] Layout per P0-T2 (0x02 + zstd, no trailer, no supersedes).
- [x] Duplicate IDs: last write wins (documented, restic semantics).
- [x] LoadAll loads existing index files (Q4 = YES) — tested against the backend.

### [x] P1-T13 — Trees and nodes (Size M) — DONE
Files: `tree/{node.go,tree.go,tree_test.go}`
- [x] Round trip preserves all fields (incl. upstream xattrs/generic attrs on parse).
- [x] Same dir contents → identical JSON bytes → identical SHA-256 (order-independent).
- [x] Field names per P0-T2 notes; canonical {"nodes":[...]} + trailing newline; strict sort with ErrTreeNotOrdered.
- [x] restic-shaped trees (incl. atime/ctime, inode, device_id) parse.

### [x] P1-T14 — Snapshot document (Size S) — DONE
Files: `snapshot/{snapshot.go,snapshot_test.go}`
- [x] Round trip preserves all fields incl. optional.
- [x] Field names per P0-T2 notes; unknown upstream fields dropped on parse.

### [x] P1-T15 — Repository lifecycle (Size M) — DONE
Files: `repository/{repository.go,config.go,init.go,snapshot.go,repository_test.go}`
- [x] Init creates exact spec §3.2 layout (all 256 data dirs), correct permissions.
- [x] Init idempotent (2nd call keeps data + same repo id).
- [x] Open: wrong password → redacted error; right password → works.
- [x] SaveBlob dedups (same bytes twice → one blob stored).
- [x] Flush: packs written BEFORE index; reopen finds all blobs.
- [x] Snapshot save/list/delete on the repository (storage ID = SHA-256 of sealed bytes).

### [x] P1-T16 — Archiver: walk, chunk, trees, snapshot (Size M) — DONE
Files: `archiver/{archiver.go,archiver_test.go}`
- [x] Backup dataset: files, subdirs, symlinks, empty files, one >16 MiB file → snapshot with right paths.
- [x] 2nd backup identical → 0 new data blobs (spec §9.4) + valid new snapshot.
- [x] 2nd backup 1 byte changed → only affected chunks new.
- [x] Cancellation mid-backup → no snapshot written, repo consistent, next backup works.

### [x] P1-T17 — Facade + app wiring (Size M) — DONE
Files: `internal/engine/restic/facade/facade.go`, `internal/app/app.go`, `internal/backup/runner.go`, `internal/config/validate.go`, `internal/cli/doctor.go`
- [x] `bqckup backup run` works with builtin engine and NO restic binary in PATH (smoke-tested; empty-PATH test).
- [x] Summary fields reach history table unchanged (runner_test + CLI smoke).
- [x] Doctor passes without restic binary for builtin engine (no binary:restic check).
- [x] `keep_last` per P0-T5: minimal retention (delete snapshot files per site tag, no prune, no silent skip).
- [x] engine selection per site (runner picks by `incremental.engine`); builtin + non-local destination = config error until L3.
- Verify: `go test -race ./internal/...` + `make verify` green; real-restic check on CLI-made repo: "no errors were found".

### [x] P1-T18 — restic_compat harness (Size S–M) — DONE
Files: `internal/engine/restic/compat_test.go` (build tag restic_compat) + CI job
NOTE: requires restic >= 0.17.0 (v2 format). Verified locally against official restic 0.19.1.
- [x] Engine repo passes `restic check` (exit 0, "no errors were found").
- [x] `restic snapshots` matches engine snapshot (id + paths).
- [x] `restic restore` → `diff -r` byte match.
- [x] 1-byte-change backup still passes `restic check`; 2 snapshots listed.
- [x] Engine opens + continues restic-made v2 repos; result passes restic check.
- [x] Tests skip cleanly without restic binary or with < 0.17.0.
- [x] CI: new restic-compat job downloads pinned restic 0.19.1 and runs the suite.
- Verify: `go test -race -v -tags=restic_compat ./internal/engine/restic/...` green.

### [x] P1-T19 — Docs, examples, final gate (Size S) — DONE
- [x] `docs/architecture.md` updated with the engine option + boundaries; `docs/configuration-v2.md` documents `engine: builtin`.
- [x] Migration notes: builtin opens real-restic v2 repos; v1 repos + s3/r2 keep `engine: restic` (architecture.md).
- [x] Spec §9 gates 1-7 all true (self-contained, init, backup, dedup, listing, binary interop, verify).
- Verify: `make verify` AND `sh scripts/check-docs.sh` green.

---

## FINAL CHECKPOINT

- [x] `make verify` green.
- [x] `sh scripts/check-docs.sh` green.
- [x] `restic_compat` tests green with the official restic 0.19.1 binary.
- [x] Archive mode regression green (untouched; full-mode tests pass).
- [x] Spec §9 acceptance gates 1-6 demonstrable (compat suite + CLI smoke + restic check "no errors").

## Deferred (do NOT build in this plan)

- L2 retention/prune (unless Q2 answer requires a minimal form).
- L3 S3/R2 backend. L4 locking. Restore. Rustic compatibility.
