# Restic Engine Library: Integration Specification

## 1. Purpose

This document specifies how `bqckup-go` will integrate the rewritten Restic engine library developed by the other team. It defines the adapter boundary, wiring strategy, configuration contract, error handling, testing approach, and doctor diagnostics.

## 2. Scope

**In scope:**
- Library adapter implementing `restic.Engine` interface.
- Factory wiring in `internal/app`.
- Doctor diagnostics update.
- Configuration: no new YAML fields required (the existing `incremental.engine: restic` and `incremental.password_env` fields are sufficient).

**Out of scope:**
- The library itself (owned by the other team).
- Restore operations (deferred to a later milestone).
- New CLI commands beyond existing `backup run` and `doctor`.

## 3. Package Layout

```
internal/backup/restic/
├── adapter.go            # Process adapter (current, shells out to restic binary)
├── adapter_test.go       # Process adapter tests
├── library_adapter.go    # Library adapter (new, calls rewritten engine)
├── library_adapter_test.go
├── process.go            # ProcessRunner interface and osProcessRunner
├── types.go              # Engine interface, RepoConfig, BackupSpec, SnapshotSummary
└── url.go                # RepositoryURL construction (extracted if needed)
```

## 4. Engine Interface (Consumer-Owned)

The `Engine` interface lives in `internal/backup/restic/types.go` and is owned by Bqckup:

```go
type Engine interface {
    Preflight() error
    EnsureRepository(ctx context.Context, repo RepoConfig) error
    BackupFiles(ctx context.Context, repo RepoConfig, spec BackupSpec) (SnapshotSummary, error)
    ApplyRetention(ctx context.Context, repo RepoConfig, keepLast int, siteName string) error
    Unlock(ctx context.Context, repo RepoConfig) error
}
```

Both the process adapter and the library adapter implement this interface. The runner (`internal/backup/runner.go`) only depends on `Engine`, never on a concrete adapter.

## 5. Library Adapter Design

### 5.1 Constructor

```go
// NewLibraryAdapter creates an Engine backed by the rewritten restic library.
func NewLibraryAdapter() *LibraryAdapter {
    return &LibraryAdapter{}
}
```

### 5.2 Method Mapping

| Engine Method | Library Call (Expected) | Notes |
| :--- | :--- | :--- |
| `Preflight()` | No-op or version check | Library is compiled in; always available. |
| `EnsureRepository(ctx, repo)` | `engine.InitRepository(ctx, opts)` | Must be idempotent (no error if already initialized). |
| `BackupFiles(ctx, repo, spec)` | `engine.Backup(ctx, opts)` | Must return snapshot ID, file counts, and byte metrics. |
| `ApplyRetention(ctx, repo, keepLast, site)` | `engine.Forget(ctx, opts)` + `engine.Prune(ctx, opts)` | May be two calls or one combined call depending on library API. |
| `Unlock(ctx, repo)` | `engine.RemoveStaleLocks(ctx, opts)` | Must not error if no locks exist. |

### 5.3 Error Translation

The library adapter must translate library-specific errors into Bqckup's error categories:

```go
func translateError(err error) error {
    switch {
    case errors.Is(err, engine.ErrRepoNotFound):
        return apperror.Wrap(apperror.CategoryPreflight, "repository not found", err)
    case errors.Is(err, engine.ErrAuthFailed):
        return apperror.Wrap(apperror.CategoryPreflight, "authentication failed", err)
    case errors.Is(err, engine.ErrCorrupted):
        return apperror.Wrap(apperror.CategoryInternal, "repository data corrupted", err)
    default:
        return apperror.Wrap(apperror.CategoryInternal, "restic engine error", err)
    }
}
```

### 5.4 Credential Passing

Credentials are passed via `RepoConfig` struct fields, **never** via environment variables or global state:

```go
func (a *LibraryAdapter) buildOpts(repo RepoConfig) engine.Options {
    return engine.Options{
        Repository: repo.URL,
        Password:   repo.Password,
        S3: engine.S3Options{
            AccessKeyID:     repo.AccessKeyID,
            SecretAccessKey: repo.SecretAccessKey,
            Region:          repo.Region,
        },
    }
}
```

## 6. Factory Wiring (`internal/app`)

```go
func buildResticEngine(cfg config.Config) restic.Engine {
    // When library is available, prefer it.
    // The process adapter remains as a fallback.
    return restic.NewLibraryAdapter()
}
```

### 6.1 Build Tag Strategy (Optional)

If both adapters need to coexist at compile time:

```go
//go:build restic_library
// +build restic_library

func buildResticEngine(cfg config.Config) restic.Engine {
    return restic.NewLibraryAdapter()
}
```

Default build (no tag) uses the process adapter. This is optional and depends on whether the library introduces heavy transitive dependencies.

## 7. Doctor Diagnostics

### Current Behavior (Process Adapter)
```
[✓] binary:restic: found at /usr/bin/restic
```
or
```
[✗] binary:restic: restic executable not found in $PATH
```

### Updated Behavior (Library Adapter)
```
[✓] restic_engine: built-in library (version X.Y.Z)
```

### Implementation
```go
if needsRestic {
    // Check which engine implementation is active
    engine := buildResticEngine(cfg)
    if err := engine.Preflight(); err != nil {
        addCheck("restic_engine", "fail", fmt.Sprintf("restic engine unavailable: %v", err))
    } else {
        addCheck("restic_engine", "ok", "built-in library available")
    }
}
```

## 8. Testing Strategy

### 8.1 Unit Tests (Library Adapter)

Test the adapter in isolation using a mock/fake of the library's API:

```go
type fakeResticLib struct {
    initErr   error
    backupErr error
    summary   engine.Summary
}

func TestLibraryAdapter_EnsureRepository(t *testing.T) {
    t.Run("idempotent init", func(t *testing.T) { ... })
    t.Run("context cancellation", func(t *testing.T) { ... })
    t.Run("authentication failure", func(t *testing.T) { ... })
}

func TestLibraryAdapter_BackupFiles(t *testing.T) {
    t.Run("successful backup returns summary", func(t *testing.T) { ... })
    t.Run("empty include paths rejected", func(t *testing.T) { ... })
    t.Run("error translation", func(t *testing.T) { ... })
}
```

### 8.2 Integration Tests

Once the library is available, add integration tests that:
1. Initialize a local repository in `t.TempDir()`.
2. Write test files.
3. Run a backup.
4. Verify snapshot exists and file counts match.
5. Run retention and verify old snapshots are pruned.
6. Verify round-trip integrity (backup → list → verify contents).

### 8.3 Compatibility Tests

If the library claims Restic-compatible repository format:
1. Create a repository with the library.
2. Read it with upstream `restic` binary.
3. Create a repository with upstream `restic`.
4. Read it with the library.

## 9. Configuration

No new YAML fields are required. The existing configuration is sufficient:

```yaml
# sites/my-site.yaml
version: 2
site:
  name: my-site
  enabled: true
  backup_mode: incremental
  incremental:
    engine: restic
    password_env: RESTIC_PASSWORD
  sources:
    files:
      include:
        - /var/www/html
      exclude:
        - "*.tmp"
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1h
    keep_last: 7
```

The `engine: restic` field selects the Restic engine. Whether it uses the process adapter or library adapter is a compile-time / factory decision, not a config field.

## 10. Acceptance Criteria

The library adapter integration is complete when:

- [ ] `internal/backup/restic/library_adapter.go` implements `Engine` interface using the rewritten library.
- [ ] All `Engine` methods propagate `context.Context` for cancellation.
- [ ] Error translation maps library errors to `apperror` categories.
- [ ] Credentials are passed via struct fields, never environment variables.
- [ ] `bqckup doctor` reports "built-in library" when the library adapter is active.
- [ ] Unit tests cover: successful operations, idempotent init, cancellation, error translation, empty inputs.
- [ ] Integration tests verify round-trip backup/retention on a local temp repository.
- [ ] `make verify` and `sh scripts/check-docs.sh` pass cleanly.
- [ ] Process adapter remains functional and selectable as fallback.
