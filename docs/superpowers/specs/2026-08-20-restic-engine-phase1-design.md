# Spec: Pure-Go Restic Engine Rewrite (Phase 1 — L1 Core Engine)

**Date:** 2026-08-20  
**Status:** In Review  
**Target Package:** `internal/engine/restic/` in `github.com/bqckup/bqckup-go`  
**Parent Initiative:** Zero-External-Dependency Incremental Backup for Bqckup Go  
**Format Verification:** `docs/superpowers/notes/restic-format-verification.md` (all format claims verified against official restic source, commit `a80be14`, version 0.19.1-dev; that document wins on any conflict)  

---

## 1. Assumptions & Design Invariants

Before specifying the system, here are the explicit assumptions governing this design:

```text
ASSUMPTIONS & DESIGN INVARIANTS:
1. Compatibility: The repository format produced by this pure-Go engine MUST be 100% compatible with the official Restic repository format v2. Upstream `restic` binary (CLI) can inspect, list, verify (`restic check`), and restore (`restic restore`) repositories created by bqckup.
2. Dependencies: Zero external `github.com/restic/*` dependencies. The chunker (Rabin CDC), crypto (AES-256-CTR + Poly1305-AES), pack parser/builder, indexer, and archiver are 100% cleanly written in standard Go (with approved standard/fast libraries such as `golang.org/x/crypto` and `github.com/klauspost/compress/zstd`).
3. Execution Boundary: CLI-only modular monolith. No HTTP servers, background daemons, or CGO restic bindings.
4. Scope of Phase 1 (L1): Local filesystem repository initialization, file tree traversal, Rabin CDC chunking, deduplication against loaded repository index, encrypted pack/index/snapshot writing, and snapshot listing.
5. Deferred to Later Phases:
   - L2: Snapshot retention, forget, and pack pruning.
   - L3: S3/R2 remote backend implementation for the engine.
   - L4: Lock management and stale lock recovery.
   - Future: In-engine file restore operations.
6. Secret Safety: Passwords and keys exist only in memory; they are never logged, formatted into error strings, written to unencrypted disk, or leaked in process arguments.
```

---

## 2. Objective

### 2.1 Problem Statement
Currently, `bqckup-go`'s incremental backup mode relies on shelling out to an external `restic` binary installed on the host system via `exec.CommandContext`. This breaks the single-binary deployment promise of Go CLI tools, requires users to manage external tool dependencies across diverse Linux environments, and creates process overhead.

### 2.2 Desired Outcome
Provide an in-process, pure Go library under `internal/engine/restic/` that implements the Restic repository format from the ground up. In Phase 1 (L1), `bqckup` can initialize a local incremental repository, chunk files with content-defined chunking (Rabin Fingerprints), deduplicate data blobs against existing repository packs, encrypt all artifacts with authenticated encryption (AES-256-CTR + Poly1305), and record snapshot metadata.

### 2.3 User Story
As a system administrator running `bqckup backup run <site>`, when `backup_mode: incremental` is configured with a local storage destination, `bqckup` performs fast, deduplicated, encrypted incremental backups without requiring `restic` to be installed on the host machine.

---

## 3. Tech Stack & Verification Commands

### 3.1 Dependencies
- **Language:** Go 1.26 (toolchain compliant with Go 1.22+)
- **Standard Library:** `crypto/aes`, `crypto/cipher`, `crypto/rand`, `crypto/sha256`, `encoding/binary`, `encoding/hex`, `encoding/json`, `io`, `os`, `path/filepath`, `sync`, `time`
- **Crypto Extension:** `golang.org/x/crypto/scrypt`, `golang.org/x/crypto/poly1305`
- **Compression:** `github.com/klauspost/compress/zstd` (standard high-performance zstd encoder/decoder used in Go ecosystems for Restic v2 format)
- **Testing:** `github.com/stretchr/testify`

### 3.2 Verification Commands
```bash
# Code formatting and static analysis
make fmt
make vet

# Unit and contract tests with race detector
go test -race -v ./internal/engine/restic/...

# Integration tests verifying compatibility against installed restic binary (when available)
go test -race -v -tags=restic_compat ./internal/engine/restic/...

# Full repository verification gate
make verify
sh scripts/check-docs.sh
```

---

## 4. Project Structure

The pure-Go restic engine lives within the internal tree of `bqckup-go`. It is decoupled from Cobra commands, Viper configs, and GORM database models:

```text
internal/
├── engine/
│   └── restic/
│       ├── doc.go                 # Package overview and architectural notes
│       ├── types.go               # ID, BlobType, Blob, Handle, RepoConfig, BackupSpec, SnapshotSummary
│       ├── errors.go              # Sentinel errors (ErrInvalidPassword, ErrCorrupted, ErrRepoNotFound, etc.)
│       │
│       ├── crypto/                # Cryptographic primitives & key derivation
│       │   ├── key.go             # MasterKey struct, KDF (scrypt), key generation
│       │   ├── cipher.go          # AES-256-CTR + Poly1305-AES encrypt/decrypt primitives
│       │   ├── key_file.go        # JSON key file format parsing and serialization
│       │   └── cipher_test.go     # Vectors and round-trip crypto tests
│       │
│       ├── chunker/               # Rabin Fingerprint Content-Defined Chunking (CDC)
│       │   ├── chunker.go         # Chunker interface and sliding window implementation
│       │   ├── polynomials.go     # Irreducible polynomial generation & validation
│       │   ├── tables.go          # Precomputed Rabin lookup tables
│       │   └── chunker_test.go    # Deterministic chunking and boundary distribution tests
│       │
│       ├── backend/               # Low-level repository storage abstraction
│       │   ├── backend.go         # Backend interface (Load, Save, Stat, List, Remove)
│       │   ├── local.go           # Atomic local filesystem backend (config, keys, data, index, snapshots)
│       │   ├── layout.go          # Path resolvers for standard repository directory layouts
│       │   └── local_test.go      # Filesystem backend contract tests
│       │
│       ├── pack/                  # Pack file format (Blobs + Header + Footer)
│       │   ├── pack.go            # Pack structure definitions & constants
│       │   ├── builder.go         # Streaming pack builder for data and tree packs
│       │   ├── parser.go          # Pack header parser from raw pack bytes
│       │   └── pack_test.go       # Pack encoding, decoding, and header verification tests
│       │
│       ├── index/                 # In-memory and persisted index catalog
│       │   ├── index.go           # Index file JSON model (packs, blobs)
│       │   ├── master_index.go    # Concurrent in-memory lookup table for deduplication
│       │   ├── encoder.go         # Zstandard-compressed and plain JSON index serializing
│       │   └── index_test.go      # Index loading, indexing, and lookup tests
│       │
│       ├── tree/                  # Tree nodes and directory representation
│       │   ├── node.go            # Node struct (files, dirs, symlinks, permissions, times)
│       │   ├── tree.go            # Tree struct (sorted list of nodes)
│       │   ├── serializer.go      # Deterministic JSON encoding & decoding of trees
│       │   └── tree_test.go       # Tree serialization and node conversion tests
│       │
│       ├── snapshot/              # Snapshot metadata
│       │   ├── snapshot.go        # Snapshot struct (time, tree ID, paths, hostname, tags)
│       │   └── snapshot_test.go   # Snapshot serialization and deserialization tests
│       │
│       ├── repository/            # High-level repository lifecycle & coordination
│       │   ├── repository.go      # Repository struct (Init, Open, MasterIndex, SaveBlob, Flush)
│       │   ├── config.go          # Repository config JSON struct (version 2, chunker_polynomial)
│       │   ├── init.go            # Idempotent repository initialization
│       │   └── repository_test.go # Full repo lifecycle tests
│       │
│       ├── archiver/              # File system scanner & backup coordinator
│       │   ├── archiver.go        # Directory walker, concurrent chunking, tree builder
│       │   ├── file_saver.go      # File reader, Rabin chunker, and blob saver
│       │   └── archiver_test.go   # Snapshot generation and incremental deduplication tests
│       │
│       └── engine.go              # Public facade implementing backup.IncrementalEngine
```

---

## 5. Architectural Specification & Subsystems

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                       bqckup-go Backup Runner                               │
│                (internal/backup/runner.go)                                  │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ calls Engine interface
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                    internal/engine/restic (Facade)                          │
│                                                                             │
│  EnsureRepository()          BackupFiles()             ListSnapshots()      │
└──────────────┬──────────────────────┬─────────────────────────┬─────────────┘
               │                      │                         │
      ┌────────┴────────┐    ┌────────┴────────┐       ┌────────┴────────┐
      ▼                 ▼    ▼                 ▼       ▼                 ▼
┌───────────┐     ┌──────────────┐     ┌─────────────┐     ┌──────────────────┐
│ Init /    │     │  Archiver    │     │ Repository  │     │   MasterIndex    │
│ Key Gen   │     │ (Tree Walk)  │     │ Coordinator │     │  (Deduplication) │
└─────┬─────┘     └──────┬───────┘     └──────┬──────┘     └────────┬─────────┘
      │                  │                    │                     │
      │                  ▼                    │                     │
      │           ┌──────────────┐            │                     │
      │           │  Rabin CDC   │            │                     │
      │           │   Chunker    │            │                     │
      │           └──────┬───────┘            │                     │
      │                  │ raw blobs          │                     │
      │                  ▼                    ▼                     │
      │           ┌──────────────────────────────────┐              │
      │           │      Pack Builder & Flusher      │◄─────────────┘
      │           │  (Packs, Blobs, Compression)     │
      │           └──────────────────┬───────────────┘
      │                              │
      ▼                              ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Crypto Layer (MasterKey)                                 │
│         Scrypt KDF  •  AES-256-CTR Encryption  •  Poly1305-AES MAC          │
└──────────────────────────────────────┬──────────────────────────────────────┘
                                       │ Encrypted IV || Ciphertext || MAC
                                       ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                    Storage Backend Interface                                │
│       Local Filesystem: config, keys/, data/xx/, index/, snapshots/         │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

### Subsystem 1: Cryptography & Key Management

#### 1.1 Data Structures & Algorithms
Restic uses a two-level key hierarchy:
1. **User Password & Key File:**
   - The user provides a plaintext password (passed through environment variable `RESTIC_PASSWORD`).
   - A `KeyFile` JSON document stored under `keys/<key_id>` contains the scrypt parameters used to derive the keys (`N`, `r`, `p`, 64-byte `salt`). The parameters live in the key file and the reader always uses what it reads. Official restic calibrates its own parameters on key creation; this engine writes the fixed, compatible values `N=65536, r=8, p=1`. Any valid parameters are accepted by restic.
   - Scrypt derives 64 bytes:
     - Bytes 0..31: AES-256 key for decrypting the master key.
     - Bytes 32..47: AES key `k` for Poly1305 (AES-128).
     - Bytes 48..63: Poly1305 key `r` (random, **unmasked** — see §1.2).
2. **Master Key:**
   - Decrypting `KeyFile.Data` reveals the `MasterKey` JSON:
     ```json
     {
       "mac": {
         "k": "<base64 16 bytes>",
         "r": "<base64 16 bytes>"
       },
       "encrypt": "<base64 32 bytes>"
     }
     ```
   - All repository data (blobs, trees, index, snapshot, config) is encrypted with `MasterKey.Encrypt` and authenticated with `MasterKey.MAC`.

#### 1.2 Poly1305 Key Masking & Calculation
Poly1305-AES MAC is computed as:
$$\text{MAC} = \text{Poly1305}_{r}(\text{Ciphertext}) + \text{AES}_{k}(\text{Nonce}) \pmod{2^{128}}$$
Where Nonce is the first 16 bytes (IV) of the ciphertext container.

**Verified against restic source** (`internal/repository/crypto/crypto.go`, `poly1305PrepareKey`): the 32-byte Poly1305 key is `r || AES_k(nonce)`, fed to `golang.org/x/crypto/poly1305`. Official restic stores `r` **unmasked** (random bytes) in the key file. The RFC 7539 clamping of `r` is applied **inside** `golang.org/x/crypto/poly1305.Sum` at use time, not before storage. Do NOT premask `r` in the key file; that would be a harmless idempotent double-mask, but it is not what restic does. (For reference, the clamp values are: `r[3]&=15, r[4]&=252, r[7]&=15, r[8]&=252, r[11]&=15, r[12]&=252, r[15]&=15`.)

#### 1.3 Envelope Format
All encrypted items in the repository have a fixed 32-byte overhead:
```text
┌──────────────────┬─────────────────────────────────┬──────────────────┐
│  IV (16 bytes)   │   Ciphertext (Variable Length)  │  MAC (16 bytes)  │
│  (Random Nonce)  │   (AES-256-CTR encrypted)       │  (Poly1305-AES)  │
└──────────────────┴─────────────────────────────────┴──────────────────┘
```

#### 1.4 Key File Representation
```go
type KeyFile struct {
    Hostname string    `json:"hostname"`
    Username string    `json:"username"`
    KDF      string    `json:"kdf"`      // "scrypt"
    N        int       `json:"N"`        // 65536
    R        int       `json:"r"`        // 8
    P        int       `json:"p"`        // 1
    Created  time.Time `json:"created"`
    Data     []byte    `json:"data"`     // Base64 encoded IV || Ciphertext || MAC of MasterKey JSON
    Salt     []byte    `json:"salt"`     // Base64 encoded 64-byte salt
}
```

---

### Subsystem 2: Content-Defined Chunking (Rabin CDC)

#### 2.1 Principle
To achieve deduplication across file shifts and edits, files are split into variable-sized chunks using a 64-byte sliding window polynomial hash (Rabin Fingerprint).

#### 2.2 Parameters
- **Min Chunk Size:** $512\text{ KiB} = 524,288\text{ bytes}$
- **Average Chunk Size:** $1\text{ MiB} = 1,048,576\text{ bytes}$
- **Max Chunk Size:** $8\text{ MiB} = 8,388,608\text{ bytes}$
- **Sliding Window Size:** $64\text{ bytes}$
- **Average Target Mask:** $\text{0x0FFFFF}$ (20 bits matching average $2^{20} = 1\text{ MiB}$)
- **Polynomial:** Degree-53 irreducible polynomial generated during repository initialization and saved in `config.ChunkerPolynomial`. Verified against `github.com/restic/chunker` (`RandomPolynomial`/`DerivePolynomial`): bits above 53 are masked off, bit 53 and bit 0 are set, and irreducibility is tested with retry. The official chunker panics if the polynomial degree exceeds 53, and `restic check` validates irreducibility when loading the config.

#### 2.3 Chunker State Machine
```go
type RabinChunker struct {
    pol        uint64
    window     [64]byte
    wpos       int
    digest     uint64
    tab        [256]uint64
    out        [256]uint64
    minSize    int
    maxSize    int
    splitMask  uint64
    chunkLen   int
}
```

Algorithm loop for each input byte $b$:
1. If $\text{chunkLen} < \text{minSize}$, add $b$ to window, update digest, increment $\text{chunkLen}$, continue.
2. Calculate leaving byte from sliding window $b_{\text{out}} = \text{window}[\text{wpos}]$.
3. Update digest:
   $$\text{digest} = ((\text{digest} \ll 8) \mid b) \oplus \text{out}[b_{\text{out}}] \oplus \text{tab}[\text{digest} \gg 56]$$
4. Replace $\text{window}[\text{wpos}] = b$; $\text{wpos} = (\text{wpos} + 1) \pmod{64}$.
5. Increment $\text{chunkLen}$.
6. If $(\text{digest} \ \& \ \text{splitMask}) == 0$ or $\text{chunkLen} \ge \text{maxSize}$:
   - Trigger split point; return chunk slice $[0 \dots \text{chunkLen}]$; reset $\text{chunkLen} = 0$, $\text{digest} = 0$.

---

### Subsystem 3: Storage Backend & Repository Layout

#### 3.1 Backend Contract
The engine consumes a clean `Backend` interface:
```go
type FileType string

const (
    TypeConfig   FileType = "config"
    TypeKey      FileType = "key"
    TypeLock     FileType = "lock"
    TypeSnapshot FileType = "snapshot"
    TypeIndex    FileType = "index"
    TypeData     FileType = "data"
)

type Handle struct {
    Type FileType
    Name string // Hexadecimal SHA-256 Storage ID (empty for TypeConfig)
}

type Backend interface {
    Save(ctx context.Context, h Handle, rd io.Reader) error
    Load(ctx context.Context, h Handle, length int, offset int64, fn func(rd io.Reader) error) error
    Stat(ctx context.Context, h Handle) (FileInfo, error)
    List(ctx context.Context, t FileType, fn func(h Handle, size int64) error) error
    Remove(ctx context.Context, h Handle) error
    IsNotExist(err error) bool
}
```

#### 3.2 Local Filesystem Layout
```text
<repo_dir>/
├── config                                    (Single encrypted file)
├── keys/
│   └── <64-hex-id>                           (Encrypted KeyFile JSON)
├── data/
│   ├── 00/ ... ff/
│   │   └── <64-hex-id>                       (Pack files: data & trees)
├── index/
│   └── <64-hex-id>                           (Encrypted Index JSON)
├── snapshots/
│   └── <64-hex-id>                           (Encrypted Snapshot JSON)
├── locks/
│   └── <64-hex-id>                           (Encrypted Lock JSON)
└── tmp/                                      (Staging directory for atomic renames)
```

#### 3.3 Atomic Writes & Permissions
- All files written to backend are staged in `<repo_dir>/tmp/<temp-uuid>` with mode `0600`.
- Upon write completion and fsync, the file is atomically renamed (`os.Rename`) to its final hash-based destination.
- All directories are created with mode `0700`.

---

### Subsystem 4: Pack & Blob Architecture

#### 4.1 Blobs
A **Blob** is a unit of deduplication:
- **Data Blob (`0b00` / `0`):** Raw byte chunk cut from a file by the CDC chunker.
- **Tree Blob (`0b01` / `1`):** JSON document representing directory nodes.
- **Compressed Data Blob (`0b10` / `2`):** Data blob compressed with zstandard (Repo v2).
- **Compressed Tree Blob (`0b11` / `3`):** Tree blob compressed with zstandard (Repo v2).

Blob ID = $\text{SHA-256}(\text{Plaintext Data})$.

#### 4.2 Pack File Layout
Pack files combine multiple encrypted blobs into a single container (default target size: 16 MiB). In repository version 2, data blobs and tree blobs are placed in separate pack files.

```text
┌─────────────────────────┬─────────────────────────┬─────────────────────────┬─────────────────────────┬──────────────┐
│     Encrypted Blob 1    │     Encrypted Blob 2    │     Encrypted Blob N    │     Encrypted Header    │ Header Length│
│ (IV || Cipher || MAC)   │ (IV || Cipher || MAC)   │ (IV || Cipher || MAC)   │ (IV || Cipher || MAC)   │ (4 bytes LE) │
└─────────────────────────┴─────────────────────────┴─────────────────────────┴─────────────────────────┴──────────────┘
```

#### 4.3 Pack Header Structure (Decrypted)
The decrypted header is an ordered sequence of blob descriptors:
```text
For each blob:
┌─────────────────┬────────────────────────────────────────────────────────────────────────┐
│ Type (1 byte)   │ Entry Details (Variable based on type)                                 │
└─────────────────┴────────────────────────────────────────────────────────────────────────┘
```
- **Uncompressed Types (`0` or `1`):**
  - Length of encrypted blob in pack ($4\text{ bytes little-endian}$)
  - Plaintext SHA-256 Hash ($32\text{ bytes}$)
- **Compressed Types (`2` or `3`):**
  - Length of encrypted blob in pack ($4\text{ bytes little-endian}$)
  - Length of uncompressed plaintext ($4\text{ bytes little-endian}$)
  - Plaintext SHA-256 Hash ($32\text{ bytes}$)

---

### Subsystem 5: Master Index & Deduplication

#### 5.1 In-Memory Master Index
To avoid querying disk or uploading redundant blobs, the engine maintains an in-memory hash index of all known blobs:
```go
type IndexEntry struct {
    PackID             ID
    Type               BlobType
    Offset             uint32
    Length             uint32
    UncompressedLength uint32
}

type MasterIndex struct {
    mu    sync.RWMutex
    blobs map[ID]IndexEntry
}
```

#### 5.2 Persisted Index File Format
When new packs are written, index files are saved under `index/<sha256>`. In format v2, the index file plaintext starts with a 1-byte version header `0x02` followed by zstandard-compressed JSON:
```json
{
  "packs": [
    {
      "id": "73d04e6125cf3c28a299cc2f3cca3b78ceac396e4fcf9575e34536b26782413c",
      "blobs": [
        {
          "id": "3ec79977ef0cf5de7b08cd12b874cd0f62bbaf7f07f3497a5b1bbcc8cb39b1ce",
          "type": "data",
          "offset": 0,
          "length": 524320,
          "uncompressed_length": 1048576
        }
      ]
    }
  ]
}
```

---

### Subsystem 6: Trees, Nodes & Snapshots

#### 6.1 Node Metadata
A node represents a single filesystem object within a directory tree:
```go
type Node struct {
    Name       string       `json:"name"`
    Type       string       `json:"type"` // "file", "dir", "symlink"
    Mode       os.FileMode  `json:"mode"`
    ModTime    time.Time    `json:"mtime"`
    AccessTime time.Time    `json:"atime,omitempty"`
    ChangeTime time.Time    `json:"ctime,omitempty"`
    UID        uint32       `json:"uid"`
    GID        uint32       `json:"gid"`
    User       string       `json:"user,omitempty"`
    Group      string       `json:"group,omitempty"`
    Inode      uint64       `json:"inode,omitempty"`
    Size       uint64       `json:"size,omitempty"`
    Links      uint64       `json:"links,omitempty"`
    LinkTarget string       `json:"linktarget,omitempty"`
    Content    []ID         `json:"content"` // Ordered list of Data Blob IDs (files only)
    Subtree    *ID          `json:"subtree,omitempty"` // Tree Blob ID (directories only)
}
```

#### 6.2 Deterministic Tree JSON
A `Tree` is a struct containing `Nodes []*Node`. Nodes are **strictly sorted lexicographically by `Name`** before serialization. The canonical form verified in restic source is `{"nodes":[...]}` plus a trailing newline, with nodes strictly increasing in byte order (restic returns `ErrTreeNotOrdered` otherwise). Our engine must produce **stable, reproducible bytes** (identical directory trees → identical Tree Blob SHA-256 IDs). Byte-equality with restic's own serialization is NOT required for compatibility — restic only needs to parse our JSON. Node type strings per restic: `"file"`, `"dir"`, `"symlink"`, `"dev"`, `"chardev"`, `"fifo"`, `"socket"`, `"irregular"`.

#### 6.3 Snapshot Document
```go
type Snapshot struct {
    Time     time.Time `json:"time"`
    Parent   *ID       `json:"parent,omitempty"`
    Tree     ID        `json:"tree"`
    Paths    []string  `json:"paths"`
    Hostname string    `json:"hostname"`
    Username string    `json:"username"`
    UID      uint32    `json:"uid,omitempty"`
    GID      uint32    `json:"gid,omitempty"`
    Tags     []string  `json:"tags,omitempty"`
    Original *ID       `json:"original,omitempty"`
}
```

---

### Subsystem 7: Backup Pipeline & Archiver

The backup process traverses file paths and executes a pipeline:

```text
┌──────────────┐
│  File Walker │ Walk directory tree, filter includes/excludes
└──────┬───────┘
       │ file paths
       ▼
┌──────────────┐
│  File Reader │ Open file with O_RDONLY, stat metadata
└──────┬───────┘
       │ stream
       ▼
┌──────────────┐
│  Rabin CDC   │ Split stream into 512KB - 8MB chunks
└──────┬───────┘
       │ chunks
       ▼
┌──────────────┐
│ Deduplication│ Check MasterIndex for SHA-256(chunk)
└──────┬───────┘
       │ if new
       ▼
┌──────────────┐
│  Compressor  │ Zstandard compress if enabled (Repo v2)
└──────┬───────┘
       │ compressed bytes
       ▼
┌──────────────┐
│  Encryptor   │ Encrypt with MasterKey (AES-256-CTR + Poly1305)
└──────┬───────┘
       │ encrypted blob
       ▼
┌──────────────┐
│ Pack Manager │ Append to current active pack; flush pack when full (>16MB)
└──────────────┘
```

#### 7.1 Tree Walk & Node Creation
1. For directories: walk children recursively.
2. For each child:
   - If symlink: record target path, type = `symlink`.
   - If regular file: stream through chunker, record blob IDs in `Node.Content`, calculate total size.
   - If sub-directory: recurse, save sub-tree blob, assign sub-tree blob ID to `Node.Subtree`.
3. Construct `Tree` with all child nodes, sort nodes by name, serialize to JSON, save as Tree Blob.
4. Top-level root directory Tree ID is saved into the `Snapshot` document.
5. Flush remaining packs, flush updated Index file, write Snapshot document.

---

## 6. Code Style & Reference Implementation Patterns

### 6.1 Cryptographic Encryption Pattern
```go
package crypto

import (
    "crypto/aes"
    "crypto/cipher"
    "crypto/rand"
    "errors"
    "fmt"
    "io"

    "golang.org/x/crypto/poly1305"
)

const (
    Extension = 32 // 16 bytes IV + 16 bytes MAC
    IVSize    = 16
    MACSize   = 16
)

// Encrypt encrypts plaintext using AES-256-CTR and appends a Poly1305-AES MAC.
// Output buffer: IV (16B) || Ciphertext (len(plaintext)) || MAC (16B)
func (k *MasterKey) Encrypt(dst, plaintext []byte) ([]byte, error) {
    totalLen := len(plaintext) + Extension
    if cap(dst) < totalLen {
        dst = make([]byte, totalLen)
    } else {
        dst = dst[:totalLen]
    }

    iv := dst[:IVSize]
    if _, err := io.ReadFull(rand.Reader, iv); err != nil {
        return nil, fmt.Errorf("generate iv: %w", err)
    }

    block, err := aes.NewCipher(k.EncryptKey[:])
    if err != nil {
        return nil, fmt.Errorf("create cipher: %w", err)
    }

    stream := cipher.NewCTR(block, iv)
    ciphertext := dst[IVSize : IVSize+len(plaintext)]
    stream.XORKeyStream(ciphertext, plaintext)

    // Compute Poly1305 MAC over ciphertext
    var macKey [32]byte
    k.derivePoly1305Key(&macKey, iv)
    
    var tag [16]byte
    poly1305.Sum(&tag, ciphertext, &macKey)
    copy(dst[IVSize+len(plaintext):], tag[:])

    return dst, nil
}
```

### 6.2 Secret Redaction in Errors
```go
package restic

import "fmt"

type RedactedError struct {
    Category string
    Message  string
    Err      error
}

func (e *RedactedError) Error() string {
    return fmt.Sprintf("%s: %s", e.Category, e.Message)
}

func (e *RedactedError) Unwrap() error {
    return e.Err
}
```

---

## 7. Testing & Verification Strategy

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│                            Testing Matrix                                   │
├────────────────────────┬────────────────────────────────────────────────────┤
│ Unit Tests             │ • Rabin CDC polynomial generation & split bounds   │
│ (In-memory / TempDir)  │ • Scrypt KDF + AES-CTR / Poly1305 test vectors     │
│                        │ • Pack builder & parser header length validation   │
│                        │ • Tree deterministic sorting & JSON hashing        │
├────────────────────────┼────────────────────────────────────────────────────┤
│ Contract Tests         │ • Local backend atomic save, stat, list, remove    │
│                        │ • MasterIndex concurrent lookup and pack mapping   │
├────────────────────────┼────────────────────────────────────────────────────┤
│ Round-Trip Tests       │ • Init repo -> Backup directory -> List snapshot   │
│                        │ • Incremental backup with modified file (dedup)    │
├────────────────────────┼────────────────────────────────────────────────────┤
│ CLI Compat Tests       │ • Run pure-Go engine backup                        │
│ (Opt-in tag)           │ • Execute `restic -r <dir> check` (must pass)      │
│                        │ • Execute `restic -r <dir> snapshots` (matches)    │
│                        │ • Execute `restic -r <dir> restore` (byte match)   │
└────────────────────────┴────────────────────────────────────────────────────┘
```

### 7.1 Cross-Verification with Official `restic` Binary
When the official `restic` binary is present on the host system, compatibility tests will:
1. Initialize a repository using the new pure-Go engine.
2. Back up a generated test dataset containing varied files, subdirectories, empty files, large files (>2 MB to force multi-chunk), and symlinks.
3. Invoke `restic check --repo <path>` with `RESTIC_PASSWORD` -> Expect exit code 0.
4. Invoke `restic restore latest --target <restore-path>` -> Compare `diff -r <original> <restore-path>`.
5. Perform a second backup with a 1-byte file change, run `restic check` -> Verify new snapshot and dedup efficiency.

---

## 8. Boundaries

### Always Do
- Enforce strict `0700` directory and `0600` file permissions on all repository data.
- Honor `context.Context` cancellation at every I/O loop (reading, chunking, encrypting, writing).
- Redact passwords, key material, and raw paths from all user-facing errors.
- Ensure all Tree structures sort nodes lexicographically by name before hashing/serialization.
- Delete temporary staged files if an error occurs during pack or index creation.

### Ask First
- Adding external third-party Go dependencies beyond standard crypto/compress.
- Changing the public `backup.IncrementalEngine` interface in `internal/backup/`.
- Changing default pack target sizes (16 MiB) or CDC chunk bounds (512 KiB / 1 MiB / 8 MiB).

### Never Do
- Never import any package from `github.com/restic/restic` or `github.com/restic/chunker`.
- Never execute shell strings or call `exec.Command("restic", ...)`.
- Never store plaintext passwords in files, structs, or logs.
- Never write directly to target pack/index filenames without using atomic temporary file staging.
- Never allow a failed backup run to produce a corrupted or partial snapshot reference.

---

## 9. Success Criteria (Acceptance Gates)

1. **Self-Contained Engine:** `internal/engine/restic` compiles cleanly with zero external restic module dependencies.
2. **Repository Init:** Calling `EnsureRepository()` creates a valid repository layout (`config`, `keys/`, `data/`, `index/`, `snapshots/`, `locks/`) on the local filesystem with an encrypted MasterKey.
3. **Incremental File Backup:** Calling `BackupFiles()` walks the source directory, chunks files with Rabin CDC, writes encrypted packs and index files, and creates a valid snapshot.
4. **Deduplication:** A second `BackupFiles()` call on identical data completes with 0 new data blobs uploaded and minimal duration.
5. **Snapshot Listing:** Calling `ListSnapshots()` parses snapshot documents and returns correct IDs, timestamps, and paths.
6. **Binary Interoperability:** Upstream `restic` CLI can run `restic check`, `restic snapshots`, and `restic restore` on repositories generated by this engine.
7. **Verification Gate:** `make verify` (fmt, vet, race tests, build) and `sh scripts/check-docs.sh` pass cleanly with 100% test pass rate.

---

## 10. Open Questions & Future Phase Roadmap

- **L2 (Retention & Pruning):** `ApplyRetention()` implementing snapshot forget rules and unreferenced pack data pruning.
- **L3 (S3 / R2 Backend):** Implementing the `Backend` interface using AWS SDK for Go v2 for direct incremental S3/R2 backup.
- **L4 (Lock Management):** Implementing cooperative locking and stale lock timeout detection in repository operations.
