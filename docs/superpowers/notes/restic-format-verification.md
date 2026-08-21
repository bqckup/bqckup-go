# Restic Format Verification Notes

**Date:** 2026-08-20
**Method:** Verified against the official restic source code, commit
`a80be1478a4c537f8396e0db2b05120aa78f11e0` (2026-08-01, version 0.19.1-dev,
cloned to `/tmp/restic-src`) and the official chunker source, commit
`2e8f53f1dad5dbdcf8aca1dbf1bee7d1fc9d8ce8` (2026-06-20, module
`github.com/restic/chunker v0.5.0`, cloned to `/tmp/chunker-src`).

This document is the source of truth for the format descriptions in
`docs/superpowers/specs/2026-08-20-restic-engine-phase1-design.md`.
When the spec and this document disagree, this document wins (it quotes the
official code). Paths below are relative to `/tmp/restic-src` unless marked
`chunker-src`.

---

## 1. Verdicts on the four doubted claims

A doubt-driven review cycle questioned four spec/correction pairs. The
official source ruled as follows:

| # | Claim in question | Verdict | Evidence |
| :--- | :--- | :--- | :--- |
| 1 | Pack header lengths are 4-byte LE (spec) vs uvarint (correction) | **Spec correct. Correction wrong.** | `internal/repository/pack/pack.go` `makeHeader()` and `parseHeaderEntry()` use `binary.LittleEndian.PutUint32/Uint32`. No uvarint anywhere in pack.go history. |
| 2 | Polynomial is 64-bit (spec) vs degree-53 (correction) | **Spec wrong. Correction substantially right.** | `chunker-src/polynomials.go` `RandomPolynomial()` / `DerivePolynomial()`: sets bit 53, masks bits above 53. `chunker-src/chunker.go:178-179` panics if `polShift > 53-8` ("the polynomial must have a degree less than or equal 53"). Detail correction: the repository config validates **irreducibility** (`internal/restic/config.go:81`), not the degree; the degree limit is enforced by the chunker at use time. |
| 3 | Index = version byte + zstd JSON (spec) vs + 4-byte length trailer (correction) | **Spec correct. Correction wrong.** | `internal/repository/repository.go` `saveUnpacked()`/`LoadUnpacked()`: no trailer in v2. Plaintext is `0x02 \|\| zstd(JSON)`; the reader derives the length from the file size minus the 32-byte crypto overhead. (The trailer idea came from the old v1 format.) |
| 4 | MAC formula + premasked `r` (spec + correction agree r is premasked) | **Formula correct. "Premasked" wrong.** | `internal/repository/crypto/crypto.go` `poly1305PrepareKey()`: `k[0:16]=R`, `k[16:32]=AES_k(nonce)`, then `poly1305.Sum(msg, k)`. So `MAC = Poly1305_r(ct) + AES_k(IV) mod 2^128` — correct. But restic stores `R` as **random unmasked bytes** (`NewRandomKey()`); the RFC 7539 clamping happens **inside** `golang.org/x/crypto/poly1305`. Premasking in the key file would be a harmless idempotent double-mask, but it is not what restic does. |

## 2. Verified format reference (v2)

### 2.1 Repository layout

`internal/backend/layout/layout_default.go`:

```text
<repo>/
├── config                  (raw JSON, encrypted; no compression, no version byte)
├── data/<xx>/<64hex>       (pack files; xx = first 2 hex chars of the name)
├── index/<64hex>
├── snapshots/<64hex>
├── keys/<64hex>
└── locks/<64hex>
```

- All 256 `data/` subdirectories are created at init (`Paths()`).
- `config` is stored under the **empty ID** (`Filename()` returns `config` for `ConfigFile`).

### 2.2 Config file

`internal/restic/config.go`:

```json
{"version":2,"id":"<random hex id>","chunker_polynomial":"<hex>"}
```

- `StableRepoVersion = 2` (new repos are v2). `MinRepoVersion=1`, `MaxRepoVersion=2`.
- `chunker_polynomial` is a degree-53 irreducible polynomial serialized as a hex string (`chunker-src/polynomials.go` `MarshalJSON`).
- On load, restic checks the version range and **irreducibility** of the polynomial (`LoadConfig`, `config.go:81`).
- The config file is the **only** unpacked file that is not zstd-compressed (`saveUnpacked`, `repository.go:500`).

### 2.3 Crypto envelope and MAC

`internal/repository/crypto/crypto.go`:

- AES-256-CTR (`aesKeySize=32`, `cipher.NewCTR`), 16-byte random nonce, 16-byte MAC, `Extension = 32`.
- Layout: `nonce (16) || ciphertext || MAC (16)`.
- MAC: `k[0:16]=R; k[16:32]=AES_k(nonce); poly1305.Sum(ciphertext, k)` → `Poly1305_r(ct) + AES_k(IV) mod 2^128`.
- `R` is random and unmasked in the key file; clamping happens inside `golang.org/x/crypto/poly1305`.

### 2.4 Key file

`internal/repository/key.go`:

```go
type Key struct {
    Created  time.Time `json:"created"`
    Username string    `json:"username"`
    Hostname string    `json:"hostname"`
    KDF  string `json:"kdf"`     // "scrypt"
    N    int    `json:"N"`
    R    int    `json:"r"`
    P    int    `json:"p"`
    Salt []byte `json:"salt"`    // 64 bytes
    Data []byte `json:"data"`    // nonce || AES-CTR(MasterKeyJSON) || MAC
}
```

- File name under `keys/` = SHA-256 of the **encrypted** file bytes
  (`saveUnpacked`, `repository.go:529`): `id = restic.Hash(ciphertext)`.
- `MasterKey` JSON: `{"mac":{"k":"<base64 16B>","r":"<base64 16B>"},"encrypt":"<base64 32B>"}`
  (`MACKey.MarshalJSON`, `crypto.go:128`).
- KDF: scrypt with 64-byte salt, 64-byte output = 32B AES key + 16B `k` + 16B `r`
  (`kdf.go:83-87`).
- **Parameters are per key file.** Restic calibrates them on creation
  (KDF timeout 500 ms, memory 60 MB, `key.go:53-58`); the reader simply uses
  the `N/r/p` stored in the key file. Our engine may write fixed
  `N=65536, r=8, p=1` — restic accepts any valid parameters it reads.

### 2.5 Pack format

`internal/repository/pack/pack.go`:

- Target pack size 16 MiB (`repository.go:28`, `DefaultPackSize`).
- Data blobs and tree blobs go into **separate** packs (separate packer
  managers in `saveAndEncrypt`).
- Pack layout: encrypted blobs in order, then the encrypted header, then a
  4-byte little-endian header length.
- Header entries (decrypted header):
  - Type byte: `0` = data, `1` = tree, `2` = compressed data, `3` = compressed tree.
  - For type 0/1: `length uint32 LE` + `ID (32 bytes)`.
  - For type 2/3: `length uint32 LE` + `uncompressed_length uint32 LE` + `ID (32 bytes)`.
  - **Fixed 4-byte little-endian lengths, NOT uvarints** (`makeHeader`,
    `parseHeaderEntry`).

### 2.6 Blobs and compression

`internal/repository/repository.go` `saveAndEncrypt()`:

- Blob ID = SHA-256 of the **plaintext** (`verifyCiphertext`: hash check).
- v2 compression rules:
  - Tree blobs (and all non-data files) are always compressed.
  - Data blobs are compressed unless the user disabled compression.
  - `UncompressedLength != 0` in the header/index marks a compressed blob.
  - Zero-length blobs are never compressed (the flag doubles as the marker).
- zstd settings used by restic: CRC disabled, 512 KiB window
  (`repository.go:340-350`). These settings do **not** affect readability —
  any valid zstd frame decodes.

### 2.7 Index files

`internal/repository/index/index.go` + `repository.go`:

- Plaintext: `0x02 || zstd(JSON)`, then encrypted like everything else.
- No length trailer.
- JSON shape:

```json
{"packs":[{"id":"<64hex>","blobs":[{"id":"<64hex>","type":"data|tree","offset":0,"length":123,"uncompressed_length":456}]}]}
```

- The `supersedes` field was **removed** in current restic. Do not write it.
- `uncompressed_length` is `omitempty`.

### 2.8 Trees and nodes

`internal/data/node.go`, `internal/data/tree.go`:

- Serialized form: `{"nodes":[...]}` plus a trailing newline
  (`TreeJSONBuilder`). Nodes must be **strictly** sorted by name in byte
  order (`AddNode` returns `ErrTreeNotOrdered` otherwise).
- Each node is `json.Marshal(Node)` in struct field order.
- Node types: `file`, `dir`, `symlink`, `dev`, `chardev`, `fifo`, `socket`,
  `irregular`.
- Notable fields: `name`, `type`, `mode,omitempty`, `mtime`, `atime`,
  `ctime`, `uid`, `gid`, `user,omitempty`, `group,omitempty`, `inode,omitempty`,
  `device_id,omitempty`, `size,omitempty`, `links,omitempty`,
  `linktarget,omitempty`, `linktarget_raw,omitempty`,
  `extended_attributes,omitempty`, `generic_attributes,omitempty`,
  `device,omitempty`, `content`, `subtree,omitempty`.
- `atime`/`ctime` are NOT omitempty in restic (always written). Either form
  parses; our engine should pick one canonical form and keep it stable.

### 2.9 Snapshots

`internal/data/snapshot.go`:

- Fields: `time`, `parent,omitempty`, `tree`, `paths`, `hostname,omitempty`,
  `username,omitempty`, `uid,omitempty`, `gid,omitempty`, `excludes,omitempty`,
  `tags,omitempty`, `original,omitempty`, `program_version,omitempty`,
  `summary,omitempty`.
- Stored under `snapshots/<64hex>` where the name is the hash of the
  encrypted bytes (same rule as keys).

### 2.10 Chunker

`chunker-src/chunker.go`:

- `MinSize = 512 KiB`, `MaxSize = 8 MiB`, `splitmask = (1<<20)-1`
  (~1 MiB mean split interval; chunk mean ≈ 1.5 MiB including MinSize), window 64 bytes.
- `RandomPolynomial()`: random **degree-53** irreducible polynomial
  (`DerivePolynomial`: mask bits above 53, set bit 53 and bit 0, test
  irreducibility, retry).
- The chunker panics if the polynomial degree exceeds 53.

### 2.11 Locks (verified 2026-08-21, restic v0.16.4 and v0.19.1)

`internal/restic/lock.go` + `internal/repository/repository.go`
(`saveUnpacked`/`loadUnpacked`):

- Lock JSON (identical in 0.16 and 0.19):

```json
{"time":"<RFC3339Nano>","exclusive":true,"hostname":"...","username":"...","pid":123,"uid":1000,"gid":1000}
```

  `uid`/`gid` have `omitempty`; the `time` field is a normal Go
  `time.Time` (RFC3339Nano).
- **Locks are NOT plaintext.** `saveUnpacked` compresses the JSON as
  `0x02 || zstd(...)` (v2 repos only) and seals it with the repository
  master key: `nonce (16) || ciphertext || MAC (16)` — the same envelope
  as §2.3. The file name is the SHA-256 of the sealed blob (content
  addressed), NOT a random ID: uniqueness comes from the random nonce and
  timestamp, so concurrent writers never collide (no CAS needed on S3).
  The plan's earlier "random-named files" description is an approximation;
  the implementation follows the verified format.
- Locking algorithm (0.19 `newLock`): check existing locks → create own
  → wait 200 ms → check again → remove own on conflict. `Refresh` writes a
  NEW lock file (new name) and removes the old one. Stale = `time.Since(Time)
  > 30 min`; restic also treats a lock as stale when its hostname matches
  the current host and the PID is no longer alive.
- Lock semantics changed in restic >= 0.17: `backup` takes an **append
  (non-exclusive)** lock so backups can run concurrently; `prune`/`forget`
  take exclusive locks. bqckup deliberately keeps backup exclusive (plan
  D5): strictly safer, and both directions still conflict correctly.

## 3. Required spec changes (from this verification)

1. **§2.2 Polynomial:** change "64-bit irreducible polynomial" to
   "degree-53 irreducible polynomial" (bit 53 set), generated like restic's
   `RandomPolynomial()`.
2. **§1.1:** reword scrypt framing — parameters live in the key file and the
   reader uses them; restic calibrates its own; our fixed `N=65536, r=8, p=1`
   is compatible. Change "Pre-mask Poly1305 key r" to "Poly1305 key r
   (random, unmasked)".
3. **§1.2:** the stored `r` is NOT premasked; clamping happens inside
   `golang.org/x/crypto/poly1305`. The spec's `maskPoly1305Key` is an
   idempotent double-mask — not needed, not what restic does.
4. **§5.2:** remove `"supersedes": []` from the index JSON example; note that
   the config file is the only unpacked file without compression; confirm no
   length trailer.
5. **§6.2:** note the canonical tree form `{"nodes":[...]}` + trailing
   newline and the full node-type list; our engine needs stable output, not
   byte-equality with restic.

## 4. Consequence for the plan and compat tests

- P0-T2 is now **complete** (this document is its deliverable).
- The installed local restic is **0.16.4** which only understands repository
  format **v1**. The `restic_compat` gate must use restic **≥ 0.17.0**
  (v2 support). The binary/version policy question (plan Q5/P0-T3 #5) must
  state `restic >= 0.17.0` as the minimum for the compat suite.
