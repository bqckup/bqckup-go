# Restic Engine Integration: Planning & Roadmap

> **Superseded (2026-08-20).** The "separate library by another team" decision in
> this document is replaced by the in-tree pure-Go engine design:
> [`superpowers/specs/2026-08-20-restic-engine-phase1-design.md`](superpowers/specs/2026-08-20-restic-engine-phase1-design.md).
> Still true: the process adapter (`internal/backup/restic/adapter.go`) remains
> the interim implementation with its consumer contract. Anything below that
> contradicts the new spec loses.

## Context

Bqckup Go currently supports two backup modes:

1. **Full (Archive):** Creates `.tar.gz` file archives per run.
2. **Incremental:** Delegates to a Restic subprocess for content-defined chunking, deduplication, and encrypted snapshots.

The incremental mode today shells out to a system-installed `restic` binary via `exec.CommandContext`. This works, but imposes a hard external dependency on the end user.

## Decision: Rewritten Restic Engine as a Go Library

After evaluating three alternatives, the supervisor has directed that:

- A **separate team** will produce a **rewritten version** of the Restic engine components required by Bqckup.
- Bqckup Go will **import this rewrite as a Go package** (e.g. `go get github.com/bqckup/restic-engine`), eliminating the subprocess boundary entirely.
- The current process-based adapter (`internal/backup/restic/`) remains as the **interim implementation** until the library is ready.

### Alternatives Considered

| Approach | Verdict | Rationale |
| :--- | :--- | :--- |
| Subprocess (`exec restic`) | Current interim | Works today but requires user to install restic. |
| `//go:embed` static binary | Rejected | Evaluated and prototyped; supervisor directed library import instead. |
| In-tree copy of restic source | Rejected | 50k+ lines of maintenance burden; Go `internal/` package restrictions block direct import. |
| **Rewritten Go library (chosen)** | **Approved** | Clean API boundary, team-owned maintenance, zero external dependency for end users, full process-level integration. |

---

## What the Rewritten Library Must Provide

The following section defines the **contract that Bqckup Go consumes**. The other team owns the implementation; this project owns the integration boundary.

### Required Capabilities

The library must expose a Go API covering these operations:

| Operation | Current Subprocess Equivalent | Purpose |
| :--- | :--- | :--- |
| **Repository Init** | `restic init --repo <url>` | Create a new backup repository if one does not exist. |
| **Backup** | `restic backup --repo <url> --json [paths...]` | Perform an incremental, deduplicated, encrypted backup of specified paths. |
| **Retention / Forget** | `restic forget --repo <url> --keep-last N --prune` | Remove old snapshots and prune unreferenced data. |
| **Unlock** | `restic unlock --repo <url>` | Remove stale repository locks after a crash. |
| **Snapshot Listing** (optional) | `restic snapshots --repo <url> --json` | List existing snapshots for diagnostics. |

### Required Interfaces (Bqckup Consumer Contract)

Bqckup already defines the consumer interface in [`internal/backup/restic/types.go`](file:///home/aexion_linggar/Documents/bqckup-go/internal/backup/restic/types.go):

```go
type Engine interface {
    Preflight() error
    EnsureRepository(ctx context.Context, repo RepoConfig) error
    BackupFiles(ctx context.Context, repo RepoConfig, spec BackupSpec) (SnapshotSummary, error)
    ApplyRetention(ctx context.Context, repo RepoConfig, keepLast int, siteName string) error
    Unlock(ctx context.Context, repo RepoConfig) error
}
```

The rewritten library adapter must satisfy this interface. The current `Adapter` struct (which shells out to `restic`) already satisfies it. The library adapter will be a second implementation.

### Required Data Types (Shared Contract)

```go
type RepoConfig struct {
    URL             string  // Local path or s3:endpoint/bucket/prefix
    Password        string  // Repository encryption password
    AccessKeyID     string  // AWS/S3/R2 credential
    SecretAccessKey string  // AWS/S3/R2 credential
    Region          string  // AWS region
}

type BackupSpec struct {
    SiteName string   // Tag for snapshot identification
    Include  []string // Paths to back up
    Exclude  []string // Glob patterns to exclude
    Tags     []string // Metadata tags
}

type SnapshotSummary struct {
    SnapshotID          string  `json:"snapshot_id"`
    FilesNew            int     `json:"files_new"`
    FilesChanged        int     `json:"files_changed"`
    FilesUnmodified     int     `json:"files_unmodified"`
    TotalFilesProcessed int     `json:"total_files_processed"`
    TotalBytesProcessed int64   `json:"total_bytes_processed"`
    DataAdded           int64   `json:"data_added"`
    TotalDuration       float64 `json:"total_duration"`
}
```

### Non-Functional Requirements for the Library

1. **Context Propagation:** All operations must accept `context.Context` and honor cancellation.
2. **Error Semantics:** Errors must be wrappable and categorizable (repository not found, authentication failure, corruption, etc.).
3. **No Global State:** The library must not use global variables, `init()` functions, or process-level signal handlers.
4. **Thread Safety:** Concurrent calls with different `RepoConfig` values must be safe.
5. **Secret Isolation:** The library must not log, print, or persist credentials.
6. **Backend Support:** Must support local filesystem and S3-compatible (including Cloudflare R2) backends.
7. **Go Module:** Published as a standalone Go module with semantic versioning.

---

## Storage Backend Support Matrix

| Backend | Repository URL Format | Required |
| :--- | :--- | :--- |
| Local filesystem | `/var/backups/bqckup/restic/site-name` | Yes (MVP) |
| AWS S3 | `s3:s3.amazonaws.com/bucket/prefix/restic/site-name` | Yes (MVP) |
| Cloudflare R2 | `s3:https://account.r2.cloudflarestorage.com/bucket/prefix/restic/site-name` | Yes (MVP) |
| SFTP | `sftp:user@host:/path` | No (future) |
| REST Server | `rest:https://host:port/path` | No (future) |

---

## Integration Architecture

```
┌──────────────────────────────────────────────────────────┐
│                    bqckup CLI binary                     │
│                                                          │
│  internal/cli ──► internal/backup/runner.go               │
│                         │                                │
│            ┌────────────┼────────────┐                   │
│            ▼            ▼            ▼                   │
│     files/archiver  db/exporter  restic.Engine           │
│                                    │                     │
│                    ┌───────────────┤                     │
│                    ▼               ▼                     │
│           [Process Adapter]  [Library Adapter]           │
│           (current interim)  (from rewritten lib)        │
│                    │               │                     │
│              exec restic      Go function calls          │
│                    │               │                     │
│                    ▼               ▼                     │
│              restic binary    restic-engine module       │
│              (system PATH)   (go get dependency)         │
└──────────────────────────────────────────────────────────┘
```

### Adapter Selection Strategy

The `internal/app/app.go` factory decides which adapter to use:

1. If the rewritten library module is available (imported and compiled in), use the **Library Adapter**.
2. If the library is not yet integrated (build tag or feature gate), fall back to the **Process Adapter** (current behavior, requires `restic` in `$PATH`).

This allows a gradual transition: the process adapter remains functional during development of the library.

---

## Migration Path

### Phase 1: Current State (Today)
- Process-based adapter shells out to `restic` binary.
- User must install `restic` separately.
- All tests pass against the process adapter via `fakeProcessRunner`.

### Phase 2: Library Development (Other Team)
- Other team develops `github.com/bqckup/restic-engine` (or equivalent module path).
- Library exposes functions matching the operations above.
- Library has its own test suite covering repository operations, chunking, encryption, and backend I/O.

### Phase 3: Library Adapter Integration (This Project)
- Add `internal/backup/restic/library_adapter.go` implementing `Engine` interface using the library.
- Wire the library adapter in `internal/app/app.go`.
- Update `bqckup doctor` to report "restic engine: built-in library" instead of checking `$PATH`.
- Process adapter remains available as a fallback or for users who prefer their own restic binary.

### Phase 4: Deprecation of Process Adapter (Future)
- Once the library adapter is stable and battle-tested, the process adapter becomes optional.
- `backup_mode: incremental` defaults to the library adapter.
- `bqckup doctor` warns if no engine is available.

---

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
| :--- | :--- | :--- | :--- |
| Library team delivers late | Medium | Medium | Process adapter remains fully functional as interim. No blocking dependency. |
| Library API doesn't match consumer contract | Low | High | This document defines the contract upfront. Both teams align on `Engine` interface before implementation. |
| Library introduces silent data corruption | Low | Critical | Library must include its own integrity test suite. Bqckup integration tests verify round-trip backup/restore. |
| Repository format incompatibility with upstream Restic | Medium | High | Library team must document format compatibility guarantees. Migration tooling if format diverges. |
| Performance regression vs subprocess | Low | Medium | Benchmark suite comparing library adapter vs process adapter throughput. |

---

## Open Questions for Supervisor / Other Team

1. **Module path:** What will the Go module path be? (e.g. `github.com/bqckup/restic-engine`)
2. **Repository format:** Will the rewritten engine produce Restic-compatible repositories, or a new format?
3. **Restore support:** Should the library include restore operations from day one, or is that a later phase?
4. **Encryption algorithm:** Will the library use the same encryption scheme as upstream Restic (AES-256-CTR + Poly1305)?
5. **Chunking algorithm:** Will it use the same CDC parameters as Restic (Rabin fingerprinting, min 512KB, max 8MB)?
6. **Timeline:** When does the other team expect to deliver a v0.1.0 with the MVP operations?
