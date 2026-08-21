# Implementation Plan: Pure-Go Restic Engine (Phase 1 / L1)

**Date:** 2026-08-20
**Status:** Draft — waiting for human review
**Spec it follows:** `docs/superpowers/specs/2026-08-20-restic-engine-phase1-design.md`
**Backlog milestone:** M11 (design cycle). Implementation milestones come AFTER approval.
**Branch:** `7-incremental-backup`

---

## 1. What is this plan about (short summary)

Today, bqckup-go uses an external program called `restic` for incremental backups.
The program must be installed on the user's computer. This is a problem.

The spec says: we will write our own version of restic's file format in pure Go,
inside this repository. Then bqckup-go does not need the external `restic` program.

Our engine must create repositories that the **official** `restic` program can
read and verify. That is the most important rule. If `restic check` says
"everything ok" on a repository made by our engine, we are compatible.

This plan splits the work into small tasks. Each task has:
- **What you do** — a short description.
- **How you know it is done** — acceptance criteria (a checklist).
- **How you verify** — the exact commands to run.
- **What you need first** — dependencies on other tasks.
- **Size** — S (small, 1-2 files), M (medium, 3-5 files), L (large, 5+ files).

## 2. Important rules BEFORE you start (the gate)

The repository has a rule (see `.agents/skills/developing-bqckup-go/references/restic-roadmap.md`
and `docs/intern-backlog.md` M11):

> **Restic is design-only until a separate proposal is approved.**

This means:

1. **Phase 0 (design review) is allowed now.** It produces documents only.
   No code changes.
2. **Phase 1 (implementation) is BLOCKED.** You must NOT edit `go.mod`,
   create Go packages, add commands, or add config fields until:
   - Phase 0 tasks are complete, AND
   - the maintainer approves the design PR.
3. The existing archive mode (`.tar.gz`) must keep working. We do not remove it.
4. Never put passwords in YAML files, logs, or error messages. Passwords live
   only in memory and in the environment variable named by
   `incremental.password_env` (forwarded to the restic subprocess as
   `RESTIC_PASSWORD`).
5. Restore (getting files back out of a repository) is a future phase.
   This plan does not build restore. When we later build restore, it must ask
   for an explicit destination and must never overwrite files silently.

If a task feels blocked, STOP and ask. Do not work around the gate.

## 3. Word list (for readers who are not native English speakers)

| Word | Simple meaning |
| :--- | :--- |
| **Blob** | One piece of data stored in the repository. A blob is a chunk of file data, or a directory listing (tree). |
| **Chunk** | A cut piece of a file. One file is cut into many chunks. |
| **CDC / chunking** | Content-Defined Chunking. We cut files into chunks by looking at the CONTENT, not at fixed sizes. If a file changes a little, most chunks stay the same, so we do not store them again. |
| **Dedup / deduplication** | Store a blob only once. If the same bytes appear again, do not store them again. Saves space. |
| **Pack** | One big file that holds many blobs. Writing many small files is slow; one big pack file is faster. Target size 16 MiB. |
| **Index** | A small file that remembers where every blob is stored: in which pack, at which offset. Like a table of contents. |
| **Tree** | A JSON document that lists the files and folders inside ONE directory. |
| **Snapshot** | One full backup point in time. It says: which tree is the root, when, which paths, on which host. |
| **Master key** | The secret key that encrypts everything in the repository. Stored in `keys/`, protected by the user password (scrypt). |
| **KDF** | Key Derivation Function. It turns the user password into a strong key. We use scrypt. |
| **MAC** | Message Authentication Code. A short tag that proves the data was not changed and was written by someone who knows the key. We use Poly1305-AES. |
| **IV** | Initialization Vector. 16 random bytes, different every time, used by the cipher. |
| **CDC chunker** | The part of the code that cuts file bytes into chunks (Rabin fingerprint). |
| **Facade** | The main door of a package. A small number of public functions; all details stay inside. |
| **Adapter** | A piece of glue code that makes two different parts talk to each other. |
| **Atomic write** | Write to a temporary file first, then rename. If the program crashes, the final file is never half-written. |
| **Milestone** | One independent piece of work, usually one PR. |
| **Gate** | A checkpoint you may not pass until a condition is true. |
| **`restic_compat` tag** | A special Go build tag. Tests with this tag run only when the official `restic` binary is installed. |

## 4. What exists today (current state)

- `internal/backup/restic/` — the current adapter. It runs the external
  `restic` binary with `exec.CommandContext`. Files: `adapter.go`, `process.go`,
  `types.go`, `adapter_test.go`.
- `internal/backup/types.go` — the `IncrementalEngine` interface the runner
  uses:
  ```go
  type IncrementalEngine interface {
      EnsureRepository(ctx context.Context, repo restic.RepoConfig) error
      BackupFiles(ctx context.Context, repo restic.RepoConfig, spec restic.BackupSpec) (restic.SnapshotSummary, error)
      ApplyRetention(ctx context.Context, repo restic.RepoConfig, keepLast int, siteName string) error
      Unlock(ctx context.Context, repo restic.RepoConfig) error
  }
  ```
  Note: `ApplyRetention` and `Unlock` are L2/L4 features in the spec. The new
  engine facade in the spec only has `EnsureRepository`, `BackupFiles`,
  `ListSnapshots`. **This mismatch must be decided in Phase 0 (Task P0-T5).**
- `internal/app/app.go` builds the engine: `restic.NewAdapter(restic.NewProcessRunner())`.
- Config today: `incremental.engine: restic` and `incremental.password_env`.
- `docs/restic-engine-planning.md` and `docs/restic-engine-integration-spec.md`
  describe an OLD decision: a separate library `github.com/bqckup/restic-engine`
  written by another team. **The new spec changes this**: the engine lives
  in-tree at `internal/engine/restic/`. The old docs must be updated (Task P0-T1).
- `go.mod` today does NOT have `golang.org/x/crypto` or
  `github.com/klauspost/compress/zstd`. The spec needs both. Adding them is a
  go.mod change → allowed only AFTER approval.

## 5. Architecture decisions

These follow the spec. If you disagree with one, say so in the design review.

| # | Decision | Why |
| :--- | :--- | :--- |
| D1 | Engine lives in-tree at `internal/engine/restic/`, zero `github.com/restic/*` imports | Single binary, no external restic needed. Supersedes the old "separate library" plan. |
| D2 | Repository format must be restic v2 compatible | The official `restic` binary must be able to check and restore our repositories. This is the acceptance gate. |
| D3 | Crypto: AES-256-CTR + Poly1305-AES, key from scrypt (N=65536, r=8, p=1) | Same as restic. Any other choice breaks compatibility. |
| D4 | Chunking: Rabin CDC, min 512 KiB, avg ≈ 1.5 MiB (512 KiB min + 1 MiB split-mask mean), max 8 MiB, 64-byte window | Same bounds as restic defaults. |
| D5 | Blob ID = SHA-256 of the PLAINTEXT bytes | This is how restic deduplicates. Must match exactly. |
| D6 | All writes are atomic: write to `tmp/`, fsync, rename; dirs 0700, files 0600 | A crash must never leave a half-written pack or index. |
| D7 | MasterIndex is an in-memory map guarded by `sync.RWMutex` | Fast dedup lookups; safe for concurrent chunk workers. |
| D8 | The engine facade keeps the existing `restic.RepoConfig` / `BackupSpec` / `SnapshotSummary` shapes where possible | The runner and history code keep working with small changes. Exact interface split is decided in P0-T5. |
| D9 | Context cancellation is honored in every I/O loop | Ctrl-C must stop the backup and clean up temp files. |
| D10 | Secrets (password, master key) never appear in errors, logs, or YAML | Repo rule. Use `RedactedError` pattern from the spec §6.2. |

## 6. Task list

### PHASE 0 — Design review (allowed NOW, documents only)

**Goal of Phase 0:** one design PR that the maintainer approves. After approval,
we may start Phase 1.

#### P0-T1 — Reconcile the spec with the old planning docs

**Status: DONE (2026-08-20).** The three old docs
(`restic-engine-planning.md`, `restic-engine-integration-spec.md`,
`restic-engine-library-requirements.md`) each got a "Superseded" banner
linking to the new spec and naming the parts that stay valid (process-adapter
contract, doctor diagnostics). `docs/intern-backlog.md` M11 now points at the
design artifacts. Only markdown changed; `sh scripts/check-docs.sh` passes.

**What you do:**
The repo has three old documents that describe a *separate library* written by
another team: `docs/restic-engine-planning.md`,
`docs/restic-engine-integration-spec.md`,
`docs/restic-engine-library-requirements.md`.
The new spec says the engine lives in this repo. Update the old docs to say:
"superseded by the Phase 1 in-tree design" and point to the new spec.
Update `docs/intern-backlog.md` M11 if needed (keep it design-only).

**Acceptance criteria:**
- [ ] Every old doc either gets a clear "superseded" header with a link, or its
      still-valid parts (integration contract, doctor update, test approach)
      are moved into the new design docs.
- [ ] No document claims both "separate library" and "in-tree" at the same time.
- [ ] No code, no go.mod, no config change in this task.

**Verification:**
- [ ] `sh scripts/check-docs.sh` passes.
- [ ] `git diff` shows ONLY markdown files.

**Dependencies:** None.
**Files likely touched:** `docs/restic-engine-planning.md`, `docs/restic-engine-integration-spec.md`, `docs/restic-engine-library-requirements.md`, `docs/intern-backlog.md`
**Size:** M

#### P0-T2 — Verify the format details against the official restic format

**Status: DONE (2026-08-20).** Verified against the official restic source
(commit `a80be14`, version 0.19.1-dev) and the official chunker module
(v0.5.0, commit `2e8f53f`). Results are recorded in
`docs/superpowers/notes/restic-format-verification.md`. The spec has been
corrected where it disagreed. Verdicts:

- [x] **Pack header entry lengths.** Spec was RIGHT: fixed 4-byte
      little-endian (`binary.LittleEndian.PutUint32`). The "uvarint" doubt
      was wrong — no uvarints anywhere in pack.go history. No spec change.
- [x] **Chunker polynomial degree.** Spec was WRONG: "64-bit" is actually
      degree-53 (bit 53 set, bits above masked). Config load checks
      irreducibility; the chunker enforces degree ≤ 53. Spec fixed.
- [x] **Index file layout.** Spec was RIGHT: version byte `0x02` + zstd JSON,
      and there is NO length trailer in v2. Extra facts recorded: the config
      file is the only unpacked file not compressed; `supersedes` was removed
      from the index JSON. Spec fixed (removed `supersedes`).
- [x] **Poly1305 key masking.** MAC formula was RIGHT:
      `MAC = Poly1305_r(ct) + AES_k(IV) mod 2^128`. But `r` is stored
      UNMASKED in the key file; clamping happens inside
      `golang.org/x/crypto/poly1305`. Spec fixed (no premasking).
- [x] **Key file name.** Confirmed: SHA-256 of the encrypted file bytes.
- [x] **Config JSON v2.** Confirmed: `version`, `id`, `chunker_polynomial`
      (hex string); new repos are version 2.
- [x] **Tree node JSON.** Confirmed: `{"nodes":[...]}` + trailing newline,
      strict byte-order sorting, node types `file/dir/symlink/dev/chardev/fifo/socket/irregular`.
- [x] **Snapshot JSON.** Confirmed field names and optional fields.

**Acceptance criteria:**
- [x] `docs/superpowers/notes/restic-format-verification.md` exists with every
      detail + source citation.
- [x] The spec is corrected everywhere it disagreed.

**Verification:**
- [x] Spec and notes both committed as markdown; `sh scripts/check-docs.sh`
      to be run at the end of the design PR.

**Dependencies:** None.
**Files likely touched:** spec file, `docs/superpowers/notes/restic-format-verification.md` (both done).
**Size:** M

#### P0-T3 — Answer the product decision checklist

**Status: DONE (2026-08-20).**
`docs/superpowers/notes/restic-product-decisions.md` answers all 12 questions
(YES/NO/PLAN + consequence for L1). Key answers: archive stays; no stdin;
idempotent init; L1 local-only; format v2 with restic >= 0.17.0 as minimum
reader; history unchanged; no locks in L1; minimal retention (no silent
skip); cancellation semantics; env-var credentials; migration YES for
real-restic v2 repos (MasterIndex loads existing indexes); restore deferred
with locked future rules. Open items Q1/Q2/Q3/Q5/Q6/Q7 listed with owner
= maintainer.

**What you do:**
The roadmap gate requires product decisions BEFORE the design PR. Write a
document that answers each question with a clear YES/NO/PLAN. Questions that
have no answer go into "Open Questions" for the maintainer.

Questions to answer (from `restic-roadmap.md`):
1. Archive compatibility: archive mode stays supported? (expect: YES)
2. Direct-source behavior: do we ever read from stdin / pipes? (expect: NO, files only)
3. Repository ownership and initialization: who creates the repo? What if the
   repo already exists but belongs to someone else?
4. Supported backends: L1 = local filesystem only. S3/R2 later. Confirm.
5. Binary/version policy: which restic versions must be able to read our repos?
   (CONFIRMED requirement: repos are format v2, so the minimum reader is
   restic ≥ 0.17.0. The machine's installed 0.16.4 is v1-only and cannot run
   the compat suite.)
6. Snapshots/history: does the bqckup history table keep working unchanged?
7. Locking: L1 writes no locks. What if restic itself created locks? (defer to L4)
8. Retention/forget/prune: the runner calls `ApplyRetention` today. What happens
   in L1 when `keep_last` is set? (see P0-T5, this is a real blocker)
9. Cancellation: Ctrl-C must clean temp files. Confirm the expected behavior.
10. Credentials: password comes from `RESTIC_PASSWORD` env var. Local repo
    needs no AWS keys. Confirm.
11. Migration: users who already have a repository made by the real restic
    binary — can the engine open it? (This decides whether the master index
    must load existing index files in L1.)
12. Restore: deferred. When we build it: explicit destination + no silent
    overwrite. Confirm.

**Acceptance criteria:**
- [ ] One document `docs/superpowers/notes/restic-product-decisions.md` with
      all 12 answers.
- [ ] Unanswered questions are listed with an owner (maintainer or user).

**Verification:**
- [ ] `sh scripts/check-docs.sh` passes.

**Dependencies:** None (parallel with P0-T1, P0-T2).
**Files likely touched:** one new notes file.
**Size:** S

#### P0-T4 — Threat model for passwords and credentials

**Status: DONE (2026-08-20).** `docs/superpowers/notes/restic-threat-model.md`
has the asset table (asset / where / who can read / leak impact / mitigation)
covering password, master key, key file, repo data, errors/logs/history,
subprocess env, tmp files, plus cross-cutting memory-hygiene rules. Matches
spec §1.6.

**What you do:**
Write a short threat model document. List every place a secret touches the
system and how we protect it:

- User password: read from env var, held in memory, passed to scrypt, then
  zeroed. Never logged.
- Master key: decrypted in memory only, never written to disk in plaintext.
- Key file on disk: encrypted, mode 0600.
- Repository directory: mode 0700.
- Error messages: use `RedactedError`; never include the password, key bytes,
  or raw AWS credentials.
- Subprocess (until the process adapter is retired): env vars only, never
  command-line arguments.

**Acceptance criteria:**
- [ ] Document lists: asset, where it lives, who can read it, what happens if
      it leaks, and the mitigation.
- [ ] The spec's "Secret Safety" invariant (§1.6) matches the document.

**Verification:**
- [ ] `sh scripts/check-docs.sh` passes.

**Dependencies:** None.
**Files likely touched:** one new notes file.
**Size:** S

#### P0-T5 — Design the adapter boundary and the retention/unlock problem

**Status: DONE (2026-08-20).**
`docs/superpowers/notes/restic-adapter-boundary-design.md` (spec addendum)
records the recommendations:
- **Problem A → option (a):** the builtin engine implements all 4 existing
  `backup.IncrementalEngine` methods — no interface change. `ApplyRetention`
  gets a minimal L1 form (delete snapshot files beyond `keep_last` for the
  site tag; no prune until L2). `Unlock` is a documented no-op (the runner
  never calls it; the engine writes no locks). `ListSnapshots` lives on the
  facade only (answers Q8).
- **Problem B → new config value** `incremental.engine: builtin` (default
  stays `restic`). Rejected: auto-by-storage, replace-adapter. Process
  adapter kept for s3/r2, v1 repos, and fallback. Doctor: `builtin` needs no
  restic binary check; validates local storage + password_env.
- Plan consequence recorded: P1-T12 must load existing index files at open.

**What you do:**
This is the most important design task. Two problems need a written answer:

**Problem A — interface mismatch.**
The runner uses `backup.IncrementalEngine` with 4 methods. The spec's facade
has 3 different methods (`EnsureRepository`, `BackupFiles`, `ListSnapshots`).
`ApplyRetention` (L2) and `Unlock` (L4) are not in the spec's L1 scope.
Options to consider:
- (a) Extend the facade so the pure-Go engine also implements
  `ApplyRetention` and `Unlock` in L1 (simple forms: delete snapshot files +
  no-op unlock). This grows L1 scope — the spec says these are L2/L4.
- (b) Keep using the process adapter for retention/unlock when the builtin
  engine is active. Simple, but keeps the external binary requirement.
- (c) Split the interface: `IncrementalEngine` (L1) + optional
  `RetentionEngine`. The runner checks capability. Cleanest, but changes a
  public internal interface (needs approval per spec §8 "Ask First").
Write the recommendation with reasons.

**Problem B — engine selection.**
Config today says `incremental.engine: restic`. How does a user choose the
builtin engine? Options: new value `engine: builtin`; automatic by storage
type (local → builtin); or replace the process adapter entirely. Also: what
does `doctor` preflight check for each engine (today it checks for the restic
binary in PATH)?

**Acceptance criteria:**
- [ ] A design section in the spec (or a short addendum) states: the chosen
      interface shape, the chosen engine-selection config, and the doctor
      behavior per engine.
- [ ] The runner flow for `keep_last` in L1 is defined (no silent skip of a
      configured policy).
- [ ] No code in this task — only the written design.

**Verification:**
- [ ] Spec or addendum updated; `sh scripts/check-docs.sh` passes.

**Dependencies:** P0-T3 (product decisions #6, #8, #11).
**Files likely touched:** spec file or one addendum file.
**Size:** M

#### P0-T6 — Design the test strategy (network-free + restic compat)

**Status: DONE (2026-08-20).** `docs/superpowers/notes/restic-test-strategy.md`
covers unit tests per package, contract tests (backend shared suite, index↔
pack resolution), round-trip tests (init→backup→list, dedup gate, 1-byte
change, cancellation), the `restic_compat` flow (check → snapshots → restore
→ diff -r), the skip-vs-fail rule (no binary or < 0.17.0 → skip; >= 0.17.0 →
failures are real), and the CI plan (existing `verify` job unchanged; new
`restic-compat` job downloads a pinned restic >= 0.17.0 tarball).

**What you do:**
Write the test strategy document. It must cover:

- **Unit tests (no network, no restic binary):** crypto vectors and round
  trips; chunker determinism and boundary bounds; pack build/parse; tree
  sorting and stable JSON; index encode/decode; backend atomic save/stat/
  list/remove; master index concurrent lookups (race detector).
- **Contract tests:** local backend; master index ↔ pack mapping.
- **Round-trip tests:** init repo → backup a temp directory → list snapshots;
  second backup with one modified file → dedup verified (0 new data blobs).
- **CLI compat tests (opt-in, tag `restic_compat`):** only run when the
  official `restic` binary exists. Flow: our engine creates repo → `restic
  check` exit 0 → `restic snapshots` → `restic restore` → `diff -r` byte
  match. These tests are skipped (not failed) when the binary is missing.
- **CI plan:** which commands run in CI (`make verify`, engine tests), and
  how a restic binary gets installed for the compat job.

**Acceptance criteria:**
- [ ] Document lists every test file planned per package, matching the spec §7
      matrix.
- [ ] The compat test skip-vs-fail rule is written down.

**Verification:**
- [ ] `sh scripts/check-docs.sh` passes.

**Dependencies:** P0-T2 (format notes decide the compat expectations).
**Files likely touched:** one new notes file.
**Size:** S

#### P0-T7 — Write the design PR and get approval

**What you do:**
Assemble Phase 0 output into ONE design-only PR:
- the corrected spec,
- format verification notes,
- product decisions,
- threat model,
- adapter boundary design,
- test strategy,
- the milestone split below (Phase 1 tasks as follow-up milestones).
Reference M11 in the backlog. Ask the maintainer for approval.

**Acceptance criteria:**
- [ ] PR contains only markdown. `make verify` unaffected.
- [ ] Maintainer approves (this is the GATE for Phase 1).
- [ ] The approved plan gets copied into `docs/superpowers/plans/` following
      the repo naming convention.

**Verification:**
- [ ] `sh scripts/check-docs.sh` passes on the PR branch.

**Dependencies:** P0-T1 … P0-T6.
**Files likely touched:** docs only.
**Size:** M

---

### Checkpoint after Phase 0 (GATE — do not cross without approval)

- [ ] Spec corrected and internally consistent (P0-T2).
- [ ] Product decisions answered or explicitly escalated (P0-T3).
- [ ] Retention/unlock and engine-selection design written (P0-T5).
- [ ] Threat model + test strategy written (P0-T4, P0-T6).
- [ ] Design PR approved by the maintainer (P0-T7).
- [ ] Archive mode still works; no code changed.

### PHASE 1 — Build the engine (BLOCKED until the gate above passes)

**Goal of Phase 1:** acceptance gates 1-6 from the spec §9.

Order: bottom-up. Each task builds on the previous one. After each task the
package still compiles and its tests pass.

#### P1-T8 — Foundation: types, errors, crypto

**What you do:**
Create `internal/engine/restic/` with `doc.go`, `types.go` (ID, BlobType,
Blob, Handle, RepoConfig, BackupSpec, SnapshotSummary), `errors.go`
(ErrInvalidPassword, ErrCorrupted, ErrRepoNotFound, RedactedError).
Create `crypto/`: master key, scrypt KDF, AES-256-CTR + Poly1305-AES
encrypt/decrypt, key file JSON parse/serialize, Poly1305 key masking.
Add test vectors and round-trip tests.

This task is the first go.mod change: add `golang.org/x/crypto` and
`github.com/klauspost/compress/zstd` (zstd needed later but harmless to add
now — or add per package as needed; decide in the PR description).

**Acceptance criteria:**
- [ ] Encrypt→Decrypt round trip works for empty, 1-byte, and 1 MiB inputs.
- [ ] MAC tamper detection: flip one ciphertext byte → decrypt fails.
- [ ] Wrong password → `ErrInvalidPassword`, message contains no secret.
- [ ] Key file JSON round trip matches the format verified in P0-T2.

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`
- [ ] `make vet` and `make fmt` pass.

**Dependencies:** Phase 0 gate. P0-T2 notes are the format source of truth.
**Files likely touched:** `internal/engine/restic/{doc.go,types.go,errors.go}`,
`internal/engine/restic/crypto/{key.go,cipher.go,key_file.go,cipher_test.go}`,
`go.mod`, `go.sum`
**Size:** M

#### P1-T9 — Rabin CDC chunker

**What you do:**
Create `chunker/`: irreducible polynomial generation/validation (degree
matching restic per P0-T2), lookup tables, sliding-window chunker with the
spec §2.3 state machine. Min 512 KiB, split mask for ~1 MiB average, max
8 MiB.

**Acceptance criteria:**
- [ ] Same input bytes → same chunk boundaries every time (deterministic).
- [ ] No chunk smaller than 512 KiB or bigger than 8 MiB (except the last
      chunk of a file).
- [ ] Boundary distribution test: over many random buffers, average chunk
      size is close to 1.5 MiB (theory: MinSize + 2^20; test band [1 MiB,
      2 MiB]).
- [ ] Inserting 1 byte in the middle of a file changes only a few chunks
      (this is the whole point of CDC — test it).

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`

**Dependencies:** P1-T8 (types). P0-T2 (polynomial degree).
**Files likely touched:** `internal/engine/restic/chunker/{chunker.go,polynomials.go,tables.go,chunker_test.go}`
**Size:** M

#### P1-T10 — Storage backend: interface + local filesystem

**What you do:**
Create `backend/`: the `Backend` interface from spec §3.1, the standard
layout path resolvers (`config`, `keys/`, `data/xx/`, `index/`, `snapshots/`,
`locks/`, `tmp/`), and the local implementation with atomic writes
(tmp file → fsync → rename), dirs 0700, files 0600.

**Acceptance criteria:**
- [ ] Save/Load round trip for every file type.
- [ ] Stat, List, Remove behave as the interface says.
- [ ] A crash simulation (kill after writing tmp file, before rename) leaves
      no partial file at the final path.
- [ ] Permissions: dirs 0700, files 0600 (test with `os.Stat`).
- [ ] Context cancellation stops a Save mid-write and removes the tmp file.

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`

**Dependencies:** P1-T8.
**Files likely touched:** `internal/engine/restic/backend/{backend.go,layout.go,local.go,local_test.go}`
**Size:** M

#### P1-T11 — Pack format: builder + parser

**What you do:**
Create `pack/`: blob type constants, streaming pack builder (data packs and
tree packs separate, 16 MiB target), header with entry layout from the
P0-T2 notes (fixed 4-byte little-endian lengths, verified — NOT uvarints), and a parser that reads a
pack file and returns all blob descriptors.

**Acceptance criteria:**
- [ ] Build a pack with N blobs → parse it → same blobs, same offsets,
      same lengths, same IDs.
- [ ] Header length trailer (last 4 bytes) is written and validated.
- [ ] Parser rejects a corrupted pack with a clear error.
- [ ] Empty pack is rejected (a pack with 0 blobs must not be written).

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`

**Dependencies:** P1-T8, P1-T9 (chunker is not needed; use fixed byte slices).
**Files likely touched:** `internal/engine/restic/pack/{pack.go,builder.go,parser.go,pack_test.go}`
**Size:** M

#### P1-T12 — Index: persisted format + in-memory master index

**What you do:**
Create `index/`: the index JSON model (packs, blobs — no supersedes field in v2), the
zstandard encoder/decoder with the exact v2 layout from P0-T2 notes, and the
concurrent MasterIndex (map[ID]IndexEntry guarded by RWMutex).

**Acceptance criteria:**
- [ ] Encode → Decode round trip preserves every entry exactly.
- [ ] MasterIndex lookup returns the right pack ID, offset, and length for
      10k entries.
- [ ] Concurrent reads + writes pass under `-race` (run a goroutine storm
      test).
- [ ] Duplicate blob IDs across packs are detected or handled per restic
      semantics (decide in test, document the choice).

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`

**Dependencies:** P1-T8, P1-T11 (entries reference pack IDs).
**Files likely touched:** `internal/engine/restic/index/{index.go,master_index.go,encoder.go,index_test.go}`
**Size:** M

#### P1-T13 — Trees and nodes

**What you do:**
Create `tree/`: Node struct (name, type, mode, times, uid/gid, size, content,
subtree), Tree struct, and deterministic JSON serializer. Nodes sorted by
name before serialization. No extra whitespace.

**Acceptance criteria:**
- [ ] Serialize → Deserialize round trip preserves all fields.
- [ ] Same directory contents → identical JSON bytes → identical SHA-256
      (stability test across random input orders).
- [ ] Node JSON field names match the format verified in P0-T2.

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`

**Dependencies:** P1-T8.
**Files likely touched:** `internal/engine/restic/tree/{node.go,tree.go,serializer.go,tree_test.go}`
**Size:** M

#### P1-T14 — Snapshot document

**What you do:**
Create `snapshot/`: Snapshot struct (time, parent, tree, paths, hostname,
username, uid/gid, tags, original) and JSON (de)serialization.

**Acceptance criteria:**
- [ ] Round trip preserves all fields including optional ones.
- [ ] JSON field names match the format verified in P0-T2.

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`

**Dependencies:** P1-T8.
**Files likely touched:** `internal/engine/restic/snapshot/{snapshot.go,snapshot_test.go}`
**Size:** S

#### P1-T15 — Repository lifecycle (the big vertical slice)

**What you do:**
Create `repository/`: RepoConfig JSON (version 2, chunker_polynomial),
idempotent Init (create layout, generate master key, write encrypted config
and key file), Open (load config, decrypt master key with password), SaveBlob
(dedup via MasterIndex, compress, encrypt, append to pack, flush at 16 MiB),
flush index files, and the flush ordering (packs before index).

**Acceptance criteria:**
- [ ] Init creates the exact directory layout from spec §3.2 with correct
      permissions.
- [ ] Init is idempotent: second call does not destroy data.
- [ ] Open with wrong password → redacted error; correct password → works.
- [ ] SaveBlob deduplicates: saving the same bytes twice stores ONE blob.
- [ ] After Flush, the repository contains pack files and an index file;
      reopening the repo finds all blobs (round trip through disk).

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`

**Dependencies:** P1-T8 … P1-T14.
**Files likely touched:** `internal/engine/restic/repository/{repository.go,config.go,init.go,repository_test.go}`
**Size:** M (this is the checkpoint task — take your time)

#### P1-T16 — Archiver: walk files, chunk, build trees, write snapshot

**What you do:**
Create `archiver/`: directory walker (include/exclude, symlink handling),
file saver (open, stat, stream through chunker, save blobs, collect IDs),
tree builder (recurse subdirectories, sort, serialize, save tree blobs), and
snapshot writer. Support parent snapshot for incremental runs.

**Acceptance criteria:**
- [ ] Backup a directory with files, subdirectories, symlinks, empty files,
      and one file > 16 MiB (multi-chunk) → snapshot exists and lists the
      right paths.
- [ ] Second backup on identical data → 0 new data blobs (dedup gate, spec
      §9.4) and a new valid snapshot.
- [ ] Second backup with 1 byte changed → only the affected chunks are new.
- [ ] Context cancellation mid-backup removes tmp files and leaves the repo
      consistent (no half-written snapshot reference).

**Verification:**
- [ ] `go test -race -v ./internal/engine/restic/...`

**Dependencies:** P1-T15.
**Files likely touched:** `internal/engine/restic/archiver/{archiver.go,file_saver.go,archiver_test.go}`
**Size:** M

#### P1-T17 — Engine facade + wire into the app (per P0-T5 design)

**What you do:**
Create `engine.go`: the facade implementing whatever interface shape P0-T5
approved (minimum: EnsureRepository, BackupFiles, ListSnapshots; retention/
unlock per the approved design). Wire construction in `internal/app`, update
config validation and doctor preflight per P0-T5, and update the runner only
where the approved design says so.

**Acceptance criteria:**
- [ ] `bqckup backup run <site>` with `backup_mode: incremental` and a local
      storage destination completes using the builtin engine (no restic
      binary needed — verify by hiding `restic` from PATH).
- [ ] Snapshot summary fields reach the history table unchanged.
- [ ] Doctor passes without a restic binary when the builtin engine is used.
- [ ] `keep_last` behavior matches the P0-T5 decision exactly.

**Verification:**
- [ ] `go test -race ./internal/...`
- [ ] `make verify`

**Dependencies:** P1-T16, P0-T5 decisions.
**Files likely touched:** `internal/engine/restic/engine.go`,
`internal/app/app.go`, `internal/config/...`, `internal/cli/...` (doctor),
`internal/backup/runner.go` (only per approved design)
**Size:** M

#### P1-T18 — Compatibility harness with the official restic binary

**What you do:**
Add `-tags=restic_compat` tests: engine creates a repo and backs up a
generated dataset; then run the official `restic` binary:
`restic check` (exit 0), `restic snapshots` (matches), `restic restore`
(diff -r byte match). Tests SKIP (not fail) when the binary is absent.
Add the CI job description.

**Acceptance criteria:**
- [ ] All three restic commands pass on an engine-made repository.
- [ ] Second backup with a 1-byte change still passes `restic check`.
- [ ] Tests skip cleanly when `restic` is not installed.

**Verification:**
- [ ] `go test -race -v -tags=restic_compat ./internal/engine/restic/...`

**Dependencies:** P1-T17.
**Files likely touched:** `internal/engine/restic/compat_test.go` (or
`repository/compat_test.go` + testdata generator), CI workflow file.
**Size:** S–M

#### P1-T19 — Docs, examples, final gate

**What you do:**
Update `docs/architecture.md`, `docs/configuration-v2.md`, user/guide docs
for the new engine option (per P0-T5 decision), migration notes (old repos
made by the real restic binary), and the release/verification checklist.

**Acceptance criteria:**
- [ ] Every public change from P1-T17 is documented in the same PR.
- [ ] Spec §9 acceptance gates 1-7 all hold.

**Verification:**
- [ ] `make verify`
- [ ] `sh scripts/check-docs.sh`

**Dependencies:** P1-T17, P1-T18.
**Files likely touched:** docs and examples.
**Size:** S

---

### Checkpoint after Phase 1 (final)

- [ ] `make verify` green (fmt, vet, race tests, build).
- [ ] `sh scripts/check-docs.sh` green.
- [ ] `restic_compat` tests green on a machine with the official binary.
- [ ] Archive mode regression test still green.
- [ ] Spec §9 acceptance gates 1-6 all demonstrably true.

## 7. Risks

| Risk | Impact | What we do about it |
| :--- | :--- | :--- |
| Format details in the spec are wrong (header lengths, polynomial degree, index trailer) | HIGH — restic check would fail | DONE: every detail verified against official restic source (`docs/superpowers/notes/restic-format-verification.md`). Spec corrected. |
| Interface mismatch (ApplyRetention/Unlock are L2/L4 but runner needs them) | HIGH — runner breaks or silently skips retention | P0-T5 designs the answer before implementation. No silent skip allowed. |
| New dependencies (x/crypto, zstd) break the "zero external deps" goal | MED — the spec already approves these two | Add them in P1-T8 with the PR description explaining why. `make verify` guards the build. |
| Old planning docs contradict the new spec (separate library vs in-tree) | MED — confusion in review | P0-T1 reconciles all docs in the design PR. |
| Concurrent chunk workers race on the MasterIndex | MED — wrong dedup decisions | RWMutex + `-race` storm test in P1-T12. |
| A file changes while we read it | LOW — same behavior as restic | Read once into the pipeline; document that we snapshot what we see (same as restic). |
| Wrong scrypt parameters make key files unreadable by restic | HIGH | Test vectors + compat test decrypts nothing but `restic check`/`restore` prove the key works. |
| Scope creep into L2/L3/L4 | MED — review friction | Every task lists "out of scope". Deferred items stay in spec §5/§10. |

## 8. Open questions (need human answers)

| # | Question | Who answers |
| :--- | :--- | :--- |
| Q1 | In-tree engine vs separate library — the spec overrides the earlier planning docs. Confirm the override. | Maintainer |
| Q2 | Retention in L1: extend the facade (a), fall back to the process adapter (b), or split the interface (c)? | Maintainer (from P0-T5) |
| Q3 | Engine selection: new `engine: builtin` value, automatic by storage type, or replace the process adapter? | Maintainer (from P0-T5) |
| Q4 | Can the builtin engine OPEN a repository created by the real restic binary (migration)? If yes, MasterIndex must load existing index files in L1. | Maintainer (from P0-T3 #11) |
| Q5 | Is `restic check` with the real binary a hard release gate, or advisory? (Compat suite requires restic >= 0.17.0 — v2 format; the installed 0.16.4 is v1-only.) | Maintainer |
| Q6 | Old repositories from the current process adapter (created by real restic, possibly on S3): keep them working via the process adapter forever, or force local for builtin? | Maintainer |
| Q7 | Compression: always on for data blobs (restic v2 default), or configurable? | Maintainer |
| Q8 | Should `ListSnapshots` be added to `backup.IncrementalEngine` (it is not there today) or live only on the facade? | Maintainer (from P0-T5) |

## 9. How to use this plan

1. **Right now:** work on Phase 0 only. No code.
2. One person takes one task at a time. Tasks marked "parallel" (P0-T1/T2/T3/T4)
   can be done by different people at the same time.
3. When a task is done, check its acceptance criteria and run its verification
   command. Paste the output into the PR.
4. After Phase 0: the design PR goes to the maintainer. Do not start P1-T8
   without approval.
5. The checklist in `tasks/todo.md` mirrors this plan. Tick items there as
   they complete.

## 10. What this plan deliberately does NOT cover (deferred)

- L2: retention/forget/prune (unless Q2 forces a minimal form).
- L3: S3/R2 backend.
- L4: lock management and stale lock recovery.
- Restore operations (explicit destination + no-overwrite design comes later).
- Rustic compatibility. Never mention Rustic settings in this work.
