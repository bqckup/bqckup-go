# Restic Engine — Test Strategy (Network-Free + restic Compat)

**Date:** 2026-08-20
**Status:** Draft — part of the Phase 0 design PR
**Scope:** L1 pure-Go engine (`internal/engine/restic/`) plus the compat
gate against the official restic binary.
**Ground truth for format expectations:** `restic-format-verification.md`.
**Repo rules:** tests run network-free by default; secrets never appear in
fixtures or output; `go test -race` is the standard.

## 1. Unit tests (no network, no restic binary, in-memory + `t.TempDir()`)

| Package | Tests |
| :--- | :--- |
| `crypto/` | KDF: scrypt N=65536,r=8,p=1 derives 64 bytes deterministically from (password, salt). Encrypt→Decrypt round trip for empty, 1 byte, 1 MiB. Flip one ciphertext byte → decrypt fails (MAC). Wrong password → `ErrInvalidPassword` with a redacted message. Key-file JSON round trip matches the verification notes (§2.4: field names, base64 data/salt, key filename = SHA-256 of encrypted bytes). |
| `chunker/` | Deterministic boundaries (same input twice → identical chunks). No chunk < 512 KiB or > 8 MiB except the final one. Average ≈ 1 MiB over random buffers (tolerance ±10%). One-byte insert mid-file changes at most a handful of chunks. Polynomial: generated polys are degree-53, irreducible, round-trip through the config hex format. |
| `pack/` | Build N blobs → parse → same offsets/lengths/IDs (round trip). Header entry layout: fixed 4-byte LE lengths, types 0/1/2/3 with `uncompressed_length` only on compressed. 4-byte header-length trailer written and validated. Corrupted trailer/header → clear error. Empty pack rejected. |
| `tree/` | Serialize→deserialize preserves every field. Same directory contents in random order → identical JSON bytes → identical SHA-256 (stability). Node type strings exactly `file/dir/symlink/dev/chardev/fifo/socket/irregular`. Canonical form `{"nodes":[...]}` + trailing newline. |
| `snapshot/` | Round trip with all fields + optional ones (`parent`, `tags`, `original`). Field names per verification notes §2.9. |
| `index/` | Encode→decode round trip exact (version byte `0x02` + zstd JSON, no trailer, no `supersedes`). `MasterIndex` lookup for 10k entries returns the right pack/offset/length. Duplicate blob ID across packs: last write wins, documented in the test. Goroutine storm (concurrent readers + writers) under `-race`. |
| `backend/` | Save/Load round trip per file type; Stat/List/Remove contract; atomic write (simulate crash after tmp write, before rename → no partial file at the final path); dirs 0700, files 0600 verified with `os.Stat`; cancellation mid-Save removes the tmp file. |
| `repository/` | Init creates the exact layout + permissions; init idempotent (2nd call keeps data); open with wrong password → redacted error; SaveBlob dedups (same bytes twice → one blob); flush writes packs BEFORE index; reopen finds all blobs. |

## 2. Contract tests

- `backend`: one shared test suite run against the local implementation (and,
  later, S3/R2) — same expectations, no per-backend test drift.
- `index ↔ pack`: every `IndexEntry` written by a flush resolves to a real
  blob in a real pack file on disk (offset+length read back and hash-match
  the plaintext after decrypt/decompress).

## 3. Round-trip tests (integration, still network-free)

1. Init a repo in `t.TempDir()` → backup a generated dataset (files,
   subdirs, symlinks, empty files, one file > 16 MiB) → list snapshots.
2. Second backup, identical data → **0 new data blobs** + valid new snapshot
   (spec §9.4 dedup gate).
3. Second backup, 1 byte changed → only the affected chunks are new.
4. Cancellation mid-backup → tmp files gone, repo still passes its own open
   + list (consistent).

## 4. CLI compat tests (opt-in, tag `restic_compat`, require restic >= 0.17.0)

Flow, using the official binary only as the READER:

1. Engine creates a repo and backs up the generated dataset.
2. `restic -r <repo> check` → exit 0.
3. `restic -r <repo> snapshots --json` → snapshot matches what the engine
   wrote (time, paths, tree).
4. `restic -r <repo> restore latest --target <dir>` → `diff -r` byte match
   with the original dataset.
5. Second backup with a 1-byte change → `restic check` still exit 0, two
   snapshots listed.

**Skip-vs-fail rule (written down):**

- `restic` not in PATH or not executable → `t.Skip` (never fail).
- `restic` present but `restic version` < 0.17.0 (v1-only, e.g. the local
  0.16.4) → `t.Skip` with a message stating the minimum version. Never
  fail: an old binary is an environment fact, not a code defect.
- `restic` >= 0.17.0 present → tests RUN and failures are real failures.
- The password for compat tests comes from the process env of the test run
  (`RESTIC_PASSWORD` or a test-set env), never a fixture literal.

## 5. CI plan

- Existing `verify` job (`.github/workflows/ci.yml`) is unchanged: `make
  verify` runs all non-compat engine tests with `-race` — no network, no
  binary needed.
- New `restic-compat` job (added in P1-T18):
  - runs on PR + push to main, in parallel with `verify`;
  - downloads the official restic >= 0.17.0 release tarball from the restic
    GitHub releases (pinned version), extracts the binary, puts it on PATH;
  - runs `go test -race -tags=restic_compat ./internal/engine/restic/...`;
  - no secrets involved: local repo + generated dataset only.
- Local runs: `RESTIC_PASSWORD=test go test -race -tags=restic_compat ./internal/engine/restic/...`
  after installing a restic >= 0.17.0 binary (the system 0.16.4 cannot run
  the suite — the tests will skip with a message).
