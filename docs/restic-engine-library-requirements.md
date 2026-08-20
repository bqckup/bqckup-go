# Restic Engine Library: Requirements for the Other Team

> **Superseded (2026-08-20).** This contract was written for an external team
> building a separate `github.com/bqckup/restic-engine` module. That decision is
> reversed: the engine is developed in-tree at `internal/engine/restic/`. See
> [`superpowers/specs/2026-08-20-restic-engine-phase1-design.md`](superpowers/specs/2026-08-20-restic-engine-phase1-design.md).
> Kept as a historical record only; no requirement below is binding for the
> in-tree engine.

## Overview

This document defines what Bqckup Go needs from the rewritten Restic engine library. It is intended to be shared with the other team as their implementation contract.

## Module & Packaging

- **Language:** Go (same as Bqckup Go).
- **Module:** Published as a standalone Go module (e.g. `github.com/bqckup/restic-engine`).
- **Versioning:** Semantic versioning (`v0.1.0` for MVP, `v1.0.0` for production-stable).
- **Dependencies:** Minimize transitive dependencies. Avoid CGO if possible.
- **Go Version:** Must support Go 1.22+ (match Bqckup Go's `go.mod`).

## Required API Surface

The library must export a package (e.g. `engine`) with the following capabilities.

### Repository Management

```go
// InitRepository creates a new encrypted backup repository.
// Must be idempotent: if the repository already exists, return nil (not an error).
func InitRepository(ctx context.Context, opts RepositoryOptions) error
```

### Backup

```go
// Backup performs an incremental backup of the specified paths.
// Returns a summary with snapshot ID, file counts, and byte metrics.
func Backup(ctx context.Context, opts BackupOptions) (BackupSummary, error)
```

### Retention

```go
// Forget removes snapshots beyond the retention policy.
// Prune removes unreferenced data packs from the repository.
// These may be separate functions or combined.
func Forget(ctx context.Context, opts ForgetOptions) error
func Prune(ctx context.Context, opts PruneOptions) error
```

### Lock Management

```go
// RemoveStaleLocks removes stale locks from the repository.
// Must not error if no locks exist.
func RemoveStaleLocks(ctx context.Context, opts RepositoryOptions) error
```

### Snapshot Listing (Optional, Recommended)

```go
// ListSnapshots returns metadata for all snapshots in the repository.
func ListSnapshots(ctx context.Context, opts RepositoryOptions) ([]Snapshot, error)
```

## Required Data Structures

### Repository Options

```go
type RepositoryOptions struct {
    // URL is the repository location.
    // Local: "/var/backups/restic/site-name"
    // S3:    "s3:https://endpoint/bucket/prefix"
    URL      string
    Password string

    // S3-compatible credentials (ignored for local repositories)
    S3AccessKeyID     string
    S3SecretAccessKey string
    S3Region          string
}
```

### Backup Options

```go
type BackupOptions struct {
    RepositoryOptions

    // Paths to include in the backup
    Include []string
    // Glob patterns to exclude
    Exclude []string
    // Tags to attach to the snapshot
    Tags []string
}
```

### Backup Summary

```go
type BackupSummary struct {
    SnapshotID          string
    FilesNew            int
    FilesChanged        int
    FilesUnmodified     int
    TotalFilesProcessed int
    TotalBytesProcessed int64
    DataAdded           int64
    TotalDuration       float64 // seconds
}
```

### Forget Options

```go
type ForgetOptions struct {
    RepositoryOptions

    // KeepLast specifies how many recent snapshots to retain.
    KeepLast int
    // TagFilter limits forget to snapshots matching this tag.
    TagFilter string
    // Prune controls whether unreferenced data is removed after forget.
    Prune bool
}
```

### Error Types

The library should define sentinel errors for categorization:

```go
var (
    ErrRepoNotFound     = errors.New("repository not found")
    ErrRepoExists       = errors.New("repository already exists")    // NOT used by Init (which is idempotent)
    ErrAuthFailed       = errors.New("authentication failed")
    ErrCorrupted        = errors.New("repository data corrupted")
    ErrLocked           = errors.New("repository is locked")
    ErrInvalidPassword  = errors.New("invalid repository password")
    ErrBackendUnavail   = errors.New("storage backend unavailable")
)
```

## Non-Functional Requirements

### Context & Cancellation
- Every exported function must accept `context.Context` as its first argument.
- Cancellation must stop in-progress I/O within a reasonable time (< 5 seconds).
- No goroutines must leak after context cancellation.

### Thread Safety
- Concurrent calls with **different** `RepositoryOptions` must be safe.
- Concurrent calls with the **same** `RepositoryOptions` must either be safe or return `ErrLocked`.

### No Global State
- No `init()` functions that modify global state.
- No package-level variables that affect behavior.
- No direct use of `os.Stdout`, `os.Stderr`, or `log.Default()`.

### Secret Safety
- The library must **never** log, print, or persist passwords or credentials.
- Error messages must not contain credential values.
- Debug/trace logging (if any) must be opt-in and must redact secrets.

### Performance Targets (Informational)

These are informational targets, not hard requirements for MVP:

| Operation | Target | Notes |
| :--- | :--- | :--- |
| Backup 1GB unchanged data | < 5 seconds | Deduplication should skip unchanged chunks. |
| Backup 1GB new data (local) | < 30 seconds | Bounded by disk I/O and chunking. |
| Backup 1GB new data (S3) | < 60 seconds | Bounded by network upload. |
| Repository init | < 2 seconds | One-time cost. |
| Forget + Prune (100 snapshots) | < 30 seconds | Depends on repository size. |

## Storage Backend Requirements

### Local Filesystem
- Must support arbitrary local paths.
- Must handle directory creation.
- Must use safe file permissions (no world-readable data files).

### S3-Compatible
- Must support custom endpoints (for Minio, Cloudflare R2, etc.).
- Must support path-style and virtual-hosted-style addressing.
- Must use the provided access key / secret key (not environment variables or instance metadata).
- Must handle multi-part uploads for large packs.

## Testing Requirements for the Library

The library must include its own test suite covering:

1. **Unit tests:** Chunking, encryption, index operations, pack management.
2. **Integration tests:** Full round-trip (init → backup → list → forget → prune) on local filesystem.
3. **S3 integration tests:** Optional, gated by environment variables / CI secrets.
4. **Cancellation tests:** Verify no goroutine leaks or partial writes on context cancellation.
5. **Corruption tests:** Verify detection of tampered data packs or index files.
6. **Concurrency tests:** Verify thread safety with `-race` flag.

## Delivery Milestones (Suggested)

| Milestone | Deliverable | Dependency |
| :--- | :--- | :--- |
| **L1: Core Engine** | Repository init, backup, snapshot listing on local filesystem. | None |
| **L2: Retention** | Forget + prune operations. | L1 |
| **L3: S3 Backend** | S3-compatible storage support (AWS, R2, Minio). | L1 |
| **L4: Unlock & Recovery** | Stale lock removal, repository check. | L1 |
| **L5: v0.1.0 Release** | Tagged module release with documented API. | L1–L4 |
| **L6: Restore (Future)** | Restore files from snapshot. | L5 + design review |

## Coordination Points

| Topic | Owner | Format |
| :--- | :--- | :--- |
| API contract (this document) | Bqckup team | This document, updated as needed. |
| Library implementation | Other team | Separate repository, own CI. |
| Integration adapter | Bqckup team | `internal/backup/restic/library_adapter.go` |
| Integration testing | Both teams | Bqckup runs round-trip tests against library. |
| Repository format compatibility | Other team | Document whether format is Restic-compatible. |
