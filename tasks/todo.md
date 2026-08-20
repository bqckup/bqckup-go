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

### [ ] P0-T7 — Design PR + maintainer approval (Size M, deps: P0-T1..T6) ← GATE
- [ ] One design-only PR: corrected spec + all notes + milestone split (Phase 1 below).
- [ ] PR contains only markdown; `make verify` unaffected.
- [ ] References M11 in the backlog.
- [ ] Maintainer approves.
- [ ] Approved plan copied into `docs/superpowers/plans/` with repo naming convention.
- Verify: `sh scripts/check-docs.sh` on the PR branch.

---

## GATE CHECKPOINT — do not start Phase 1 until all of these are true

- [ ] P0-T1 … P0-T7 done.
- [ ] Design PR approved by maintainer.
- [ ] Archive mode still supported; zero code changed so far.

---

## PHASE 1 — Build the engine (BLOCKED until gate)

### [ ] P1-T8 — Foundation: types, errors, crypto (Size M, deps: gate + P0-T2 notes)
Files: `internal/engine/restic/{doc.go,types.go,errors.go}`, `crypto/{key.go,cipher.go,key_file.go,cipher_test.go}`, `go.mod`, `go.sum`
- [ ] Encrypt→Decrypt round trip: empty, 1 byte, 1 MiB.
- [ ] Flipped ciphertext byte → decrypt fails (MAC works).
- [ ] Wrong password → ErrInvalidPassword, no secret in message.
- [ ] Key file JSON matches P0-T2 format notes.
- [ ] scrypt N=65536 r=8 p=1; derive 64 bytes (32 AES key + 16 poly1305 k + 16 premask r).
- Verify: `go test -race -v ./internal/engine/restic/...` + `make fmt` + `make vet`

### [ ] P1-T9 — Rabin CDC chunker (Size M, deps: P1-T8)
Files: `chunker/{chunker.go,polynomials.go,tables.go,chunker_test.go}`
- [ ] Deterministic boundaries (same input → same chunks).
- [ ] No chunk < 512 KiB or > 8 MiB (except last).
- [ ] Average ≈ 1 MiB over random buffers (tolerance in test).
- [ ] 1-byte insert mid-file changes only a few chunks.
- [ ] Polynomial degree per P0-T2 (restic-compatible), generated at repo init.
- Verify: `go test -race -v ./internal/engine/restic/...`

### [ ] P1-T10 — Backend: interface + local filesystem (Size M, deps: P1-T8)
Files: `backend/{backend.go,layout.go,local.go,local_test.go}`
- [ ] Save/Load round trip for every FileType.
- [ ] Stat/List/Remove per interface.
- [ ] Crash before rename → no partial file at final path.
- [ ] Dirs 0700, files 0600 (checked with os.Stat).
- [ ] Cancellation mid-Save removes tmp file.
- Verify: `go test -race -v ./internal/engine/restic/...`

### [ ] P1-T11 — Pack builder + parser (Size M, deps: P1-T8)
Files: `pack/{pack.go,builder.go,parser.go,pack_test.go}`
- [ ] Build N blobs → parse → same offsets/lengths/IDs.
- [ ] Header entry layout per P0-T2 (fixed 4-byte LE lengths, verified — NOT uvarints).
- [ ] Header length trailer written and validated.
- [ ] Corrupted pack → clear error; empty pack rejected.
- Verify: `go test -race -v ./internal/engine/restic/...`

### [ ] P1-T12 — Index: persisted format + MasterIndex (Size M, deps: P1-T8, P1-T11)
Files: `index/{index.go,master_index.go,encoder.go,index_test.go}`
- [ ] Encode→Decode round trip exact.
- [ ] Lookup returns right pack/offset/length for 10k entries.
- [ ] Goroutine storm test passes under `-race`.
- [ ] Load existing index files at open (migration #11 / Q4 = YES): repo made by real restic v2 → MasterIndex populated → dedup works across the boundary.
- [ ] Layout per P0-T2 (version byte + zstd + trailer).
- Verify: `go test -race -v ./internal/engine/restic/...`

### [ ] P1-T13 — Trees and nodes (Size M, deps: P1-T8)
Files: `tree/{node.go,tree.go,serializer.go,tree_test.go}`
- [ ] Round trip preserves all fields.
- [ ] Same dir contents → identical JSON bytes → identical SHA-256 (order-independent input).
- [ ] Field names per P0-T2 notes.
- Verify: `go test -race -v ./internal/engine/restic/...`

### [ ] P1-T14 — Snapshot document (Size S, deps: P1-T8)
Files: `snapshot/{snapshot.go,snapshot_test.go}`
- [ ] Round trip preserves all fields incl. optional.
- [ ] Field names per P0-T2 notes.
- Verify: `go test -race -v ./internal/engine/restic/...`

### [ ] P1-T15 — Repository lifecycle (Size M, deps: P1-T8..T14) ← CHECKPOINT
Files: `repository/{repository.go,config.go,init.go,repository_test.go}`
- [ ] Init creates exact spec §3.2 layout, correct permissions.
- [ ] Init idempotent (2nd call keeps data).
- [ ] Open: wrong password → redacted error; right password → works.
- [ ] SaveBlob dedups (same bytes twice → one blob stored).
- [ ] Flush: packs written BEFORE index; reopen finds all blobs.
- Verify: `go test -race -v ./internal/engine/restic/...`

### [ ] P1-T16 — Archiver: walk, chunk, trees, snapshot (Size M, deps: P1-T15)
Files: `archiver/{archiver.go,file_saver.go,archiver_test.go}`
- [ ] Backup dataset: files, subdirs, symlinks, empty files, one >16 MiB file → snapshot with right paths.
- [ ] 2nd backup identical → 0 new data blobs (spec §9.4) + valid new snapshot.
- [ ] 2nd backup 1 byte changed → only affected chunks new.
- [ ] Cancellation mid-backup → tmp cleaned, repo consistent.
- Verify: `go test -race -v ./internal/engine/restic/...`

### [ ] P1-T17 — Facade + app wiring (Size M, deps: P1-T16, P0-T5 decisions)
Files: `internal/engine/restic/engine.go`, `internal/app/app.go`, config/doctor/runner per P0-T5
- [ ] `bqckup backup run` works with builtin engine and NO restic binary in PATH.
- [ ] Summary fields reach history table unchanged.
- [ ] Doctor passes without restic binary for builtin engine.
- [ ] `keep_last` matches P0-T5 decision exactly.
- Verify: `go test -race ./internal/...` + `make verify`

### [ ] P1-T18 — restic_compat harness (Size S–M, deps: P1-T17)
Files: compat test(s) + CI workflow
NOTE: requires restic >= 0.17.0 (v2 format). The installed 0.16.4 is v1-only — install a newer binary for local runs.
- [ ] Engine repo passes `restic check` (exit 0).
- [ ] `restic snapshots` matches engine snapshot.
- [ ] `restic restore` → `diff -r` byte match.
- [ ] 1-byte-change backup still passes `restic check`.
- [ ] Tests skip cleanly without restic binary.
- Verify: `go test -race -v -tags=restic_compat ./internal/engine/restic/...`

### [ ] P1-T19 — Docs, examples, final gate (Size S, deps: P1-T17, P1-T18)
- [ ] `docs/architecture.md`, `docs/configuration-v2.md`, guides updated with the new engine option.
- [ ] Migration notes for existing real-restic-made repos.
- [ ] Spec §9 gates 1-7 all true.
- Verify: `make verify` AND `sh scripts/check-docs.sh`

---

## FINAL CHECKPOINT

- [ ] `make verify` green.
- [ ] `sh scripts/check-docs.sh` green.
- [ ] `restic_compat` tests green with official binary.
- [ ] Archive mode regression green.
- [ ] Spec §9 acceptance gates 1-6 demonstrable.

## Deferred (do NOT build in this plan)

- L2 retention/prune (unless Q2 answer requires a minimal form).
- L3 S3/R2 backend. L4 locking. Restore. Rustic compatibility.
