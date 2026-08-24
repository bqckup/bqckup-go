# Ponytail Audit — Execution Plan

This document is the result of a repo-wide audit of `bqckup-go` for
over-engineering. It is written so that a less capable model (or a human)
can execute the changes step by step.

**Goal:** remove dead code and duplicated code, and replace the heavy
`viper`/`mapstructure` config stack with the standard YAML library.
**No behavior may change.** All tests must pass after every step.

---

## Ground rules

1. Do one step at a time. Run the verification commands after each step.
2. If a command fails, STOP and fix it before continuing. Do not continue
   with broken code.
3. Do not refactor, rename, or "improve" anything that is not listed here.
4. Do not touch files in the "Do not touch" list at the end.
5. Every snippet below is complete. Copy it exactly.

Verification commands (run from the repo root):

```bash
gofmt -l .            # must print nothing
go vet ./...
go test ./...
go build ./...
```

Smoke test (must print `valid schema v2 configuration ...`):

```bash
go run ./cmd/bqckup config validate --config-dir ./configs
```

Before starting, run all verification commands and confirm everything is
green. That is the baseline.

---

## Step 1 — Remove dead code

### 1a. Delete the unused `Engine` interface

File: `internal/backup/restic/types.go`

Delete this whole block (nothing implements it on purpose, nothing uses it):

```go
type Engine interface {
	Preflight() error
	EnsureRepository(ctx context.Context, repo RepoConfig) error
	BackupFiles(ctx context.Context, repo RepoConfig, spec BackupSpec) (SnapshotSummary, error)
	ApplyRetention(ctx context.Context, repo RepoConfig, keepLast int, siteName string) error
	Unlock(ctx context.Context, repo RepoConfig) error
}
```

Keep `RepoConfig`, `BackupSpec`, and `SnapshotSummary`.

### 1b. Remove `Unlock` from the runner interface

File: `internal/backup/types.go`

In the `IncrementalEngine` interface, delete this line:

```go
	Unlock(ctx context.Context, repo restic.RepoConfig) error
```

The runner never calls it.

### 1c. Delete `Adapter.Unlock`

File: `internal/backup/restic/adapter.go`

Delete the whole `Unlock` method:

```go
func (a *Adapter) Unlock(ctx context.Context, repo RepoConfig) error {
	...
}
```

### 1d. Delete the `Unlock` tests

- File: `internal/backup/restic/adapter_test.go`
  Delete the whole `TestUnlock` function.
- File: `internal/backup/runner_test.go`
  In `fakeIncrementalEngine`, delete the method:

  ```go
  func (f *fakeIncrementalEngine) Unlock(_ context.Context, _ restic.RepoConfig) error {
  ```

### 1e. Delete `Unlock` and `ListSnapshots` from the facade

File: `internal/engine/restic/facade/facade.go`

Delete these two whole methods:

- `func (e *Engine) Unlock(context.Context, adaptertypes.RepoConfig) error`
  (the documented no-op)
- `func (e *Engine) ListSnapshots(ctx context.Context, repo adaptertypes.RepoConfig) ([]repository.SnapshotWithID, error)`

File: `internal/engine/restic/facade/facade_test.go`

- Delete the whole `TestUnlockIsNoOp` function.
- In the compile-time check at the end of the file, delete the line
  `Unlock(context.Context, adaptertypes.RepoConfig) error`.
- The test file calls `engine.ListSnapshots(ctx, repo)` in three places
  (around lines 49, 105, 135). Replace each call with
  `listSnapshots(t, repo)` and add this helper to the test file:

  ```go
  func listSnapshots(t *testing.T, repo adaptertypes.RepoConfig) []repository.SnapshotWithID {
  	t.Helper()
  	r, err := repository.Open(context.Background(), backend.NewLocal(repo.URL), repo.Password)
  	if err != nil {
  		t.Fatalf("open repository: %v", err)
  	}
  	snapshots, err := r.ListSnapshots(context.Background())
  	if err != nil {
  		t.Fatalf("list snapshots: %v", err)
  	}
  	return snapshots
  }
  ```

  Add any missing imports (`context`, `backend`, `repository`) — run
  `gofmt` and `go vet` and fix what they report.

### 1f. Move the pack parser into test code

The production code never reads pack files (restore is a later milestone),
but the tests use the parser to verify what the builder writes.

- Delete: `internal/engine/restic/pack/parser.go`
- Create: `internal/engine/restic/pack/parser_test.go` with the exact same
  content (same `package pack`, same functions, same imports).
- Add one comment at the top of the new file:

  ```go
  // Test-only copy of the pack reader. Move back to production code when
  // restore (L2) actually reads pack files.
  ```

All tests in `pack_test.go` keep working unchanged.

### 1g. Delete the unused `--verbose` flag

File: `internal/cli/root.go`

- Delete the field `verbose bool` from the `options` struct.
- Delete this line:

  ```go
  root.PersistentFlags().BoolVar(&opts.verbose, "verbose", false, "enable verbose diagnostics")
  ```

The flag is never read anywhere.

### 1h. Delete `tree.SortNodes`

File: `internal/engine/restic/tree/tree.go`

Delete the whole `SortNodes` function.

File: `internal/engine/restic/tree/tree_test.go`

Replace `SortNodes(tr.Nodes)` (around line 77) with:

```go
sort.Slice(tr.Nodes, func(i, j int) bool { return tr.Nodes[i].Name < tr.Nodes[j].Name })
```

Add `"sort"` to the imports of the test file if it is missing.

### 1i. Delete the unused `Type` field on index entries

File: `internal/engine/restic/index/master_index.go`

- In the `Entry` struct, delete the field `Type restic.BlobType`.
- In `Add`, delete the line `Type: entry.Type,`... concretely: delete the
  assignment `Type: blob.Type,` (it appears twice: once in `Add`, once in
  `AddIndex`).

Nothing reads `Entry.Type`.

### Verify Step 1

```bash
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

---

## Step 2 — Remove duplicated helpers

### 2a. One process runner instead of two identical files

Create: `internal/process/process.go`

```go
// Package process runs external commands for adapters that shell out to
// real binaries (restic, mysqldump, pg_dump).
package process

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// ProcessSpec describes one external command to run.
type ProcessSpec struct {
	Command string
	Args    []string
	Env     []string
	Stdout  io.Writer
	Stderr  io.Writer
}

// ProcessRunner executes external commands.
type ProcessRunner interface {
	LookPath(command string) (string, error)
	Run(ctx context.Context, spec ProcessSpec) error
}

type osProcessRunner struct{}

// NewProcessRunner returns the real OS-backed runner.
func NewProcessRunner() ProcessRunner { return osProcessRunner{} }

func (osProcessRunner) LookPath(command string) (string, error) {
	return exec.LookPath(command)
}

func (osProcessRunner) Run(ctx context.Context, spec ProcessSpec) error {
	command := exec.CommandContext(ctx, spec.Command, spec.Args...)
	command.Env = append(os.Environ(), spec.Env...)
	command.Stdout = spec.Stdout
	command.Stderr = spec.Stderr
	return command.Run()
}
```

Then:

- Delete `internal/backup/restic/process.go` and
  `internal/backup/database/process.go` (the two copies).
- File: `internal/backup/restic/adapter.go`
  - Add import: `process "github.com/bqckup/bqckup-go/internal/process"`
  - Replace `ProcessRunner` with `process.ProcessRunner` (2 places: the
    struct field and the `NewAdapter` parameter).
  - Replace `ProcessSpec` with `process.ProcessSpec` (in the `Run` calls).
  - Replace `NewProcessRunner()` with `process.NewProcessRunner()`
    (in `NewAdapter`).
- File: `internal/backup/restic/adapter_test.go`
  - Add the `process` import.
  - Replace `ProcessSpec` with `process.ProcessSpec`.
- File: `internal/backup/database/exporter.go`
  - Add the `process` import.
  - Same replacements as in `adapter.go` (`ProcessRunner`, `ProcessSpec`,
    `NewProcessRunner`).
- File: `internal/backup/database/exporter_test.go`
  - Add the `process` import and replace `ProcessSpec` references.
- File: `internal/app/app.go`
  - Add the `process` import.
  - `databaseexporter.NewProcessRunner()` → `process.NewProcessRunner()`
  - `restic.NewProcessRunner()` → `process.NewProcessRunner()`

### 2b. One redacted-error type instead of three copies

File: `internal/apperror/error.go`

Add at the end:

```go
// Hidden wraps an error with a public message. The cause is never shown
// in Error(), but errors.Is and errors.As still reach it through Unwrap.
type Hidden struct {
	Public string
	Cause  error
}

func (h *Hidden) Error() string { return h.Public }
func (h *Hidden) Unwrap() error { return h.Cause }

// Hide returns an error that shows public instead of the cause text.
func Hide(public string, cause error) error {
	return &Hidden{Public: public, Cause: cause}
}
```

Then, in each of these three files, delete the local
`redactedError`/`hiddenErr` type and its `hiddenError`/`hiddenErr`
constructor function, add the `apperror` import, and replace every
`hiddenError(message, err)` call with `apperror.Hide(message, err)`:

1. `internal/backup/restic/adapter.go` (type is `hiddenErr`, func is
   `hiddenError`)
2. `internal/backup/database/exporter.go` (type `redactedError`, func
   `hiddenError`)
3. `internal/storage/s3compat/store.go` (type `redactedError`, func
   `hiddenError`) — including the line
   `hiddenError(ErrObjectExists.Error(), ErrObjectExists)`, which becomes
   `apperror.Hide(ErrObjectExists.Error(), ErrObjectExists)`.

The engine's own `restic.RedactedError` stays as it is (it has a different
job and a `Category` field).

### 2c. One context-aware copy instead of three

Create: `internal/ctxcopy/ctxcopy.go`

```go
// Package ctxcopy provides an io.Copy that checks a context between chunks.
package ctxcopy

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Copy copies from src to dst and aborts as soon as ctx is done.
func Copy(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 128*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		read, readErr := src.Read(buffer)
		if read > 0 {
			count, writeErr := dst.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, fmt.Errorf("write: %w", writeErr)
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, fmt.Errorf("read: %w", readErr)
		}
	}
}
```

Then:

- File: `internal/storage/local/local.go`
  - Delete the local `copyWithContext` function.
  - Replace `copyWithContext(ctx, io.MultiWriter(staging, hash), source)`
    with `ctxcopy.Copy(ctx, io.MultiWriter(staging, hash), source)`.
  - Add the import `"github.com/bqckup/bqckup-go/internal/ctxcopy"`.
- File: `internal/storage/s3compat/store.go`
  - Delete the local `copyWithContext` function.
  - Replace `copyWithContext(ctx, hash, file)` with
    `ctxcopy.Copy(ctx, hash, file)`.
  - Add the import.
- File: `internal/engine/restic/backend/local.go`
  - Delete the local `copyContext` function.
  - Replace `copyContext(ctx, tmp, rd)` with `ctxcopy.Copy(ctx, tmp, rd)`.
  - Add the import.

The helper returns slightly different error texts than the old copies. If a
test asserts an exact error substring from these paths, update the expected
text to match the new helper.

### 2d. One checksum helper instead of two

Create: `internal/backup/checksum.go`

```go
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// ChecksumFile returns the hex SHA-256 and the byte size of the file at path.
func ChecksumFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", 0, fmt.Errorf("checksum: %w", copyErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("close: %w", closeErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
```

Then:

- File: `internal/backup/files/archiver.go`
  - Delete the local `checksumFile` function.
  - Replace the call `checksumFile(destination)` with
    `backup.ChecksumFile(destination)` and keep the surrounding error wrap
    (`fmt.Errorf("checksum archive: %w", err)`).
- File: `internal/backup/database/exporter.go`
  - Delete the local `checksumFile` function.
  - Replace the call `checksumFile(destination)` with
    `backup.ChecksumFile(destination)`, wrapping errors with
    `apperror.Hide("could not verify database export", err)`.

Both files already import `internal/backup`.

### 2e. One site-name rule instead of three regexes

File: `internal/config/validate.go`

- Rename the variable `safeName` to `SafeName` (exported) and update its
  uses inside this file (`safeName.MatchString` → `SafeName.MatchString`).

File: `internal/platform/lock/flock_linux.go`

- Delete the local `safeSiteName` regexp.
- Replace `safeSiteName.MatchString(site)` with
  `config.SafeName.MatchString(site)`.
- Add the import `"github.com/bqckup/bqckup-go/internal/config"`.

File: `internal/storage/s3compat/store.go`

- Delete the local `safeSiteName` regexp.
- Replace its two uses with `config.SafeName.MatchString(...)`.
- Add the `config` import.

### 2f. One identity helper instead of two

File: `internal/engine/restic/repository/init.go`

- Rename the function `currentIdentity` to `CurrentIdentity` (exported) and
  update its one call site in `saveKeyFile`.

File: `internal/engine/restic/facade/facade.go`

- Delete the `hostname()` and `username()` helper functions.
- In `BackupFiles`, replace:

  ```go
  		Hostname: hostname(),
  		Username: username(),
  ```

  with:

  ```go
  	username, hostname := repository.CurrentIdentity()
  	// (then inside the archiver.BackupSpec literal:)
  		Hostname: hostname,
  		Username: username,
  ```

- Remove the imports `os` and `os/user` from facade.go if nothing else uses
  them (run `go vet` to confirm).

### Verify Step 2

```bash
gofmt -l . && go vet ./... && go test ./... && go build ./...
```

---

## Step 3 — Replace viper/mapstructure with yaml.v3

The config loader currently uses `viper` (a big framework) plus
`mapstructure` decode hooks to read three static YAML files. The standard
`gopkg.in/yaml.v3` library (already in `go.mod` as an indirect dependency)
does everything needed with less code and far fewer dependencies.

### 3a. Add YAML decode support

Create: `internal/config/yaml.go`

```go
package config

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// StringList accepts a YAML list ("- a\n- b") or one comma-separated
// string ("a, b"). This replaces the old mapstructure string-to-slice hook.
type StringList []string

// UnmarshalYAML implements strict decoding for list values.
func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var values []string
		if err := node.Decode(&values); err != nil {
			return err
		}
		*s = values
		return nil
	case yaml.ScalarNode:
		var raw string
		if err := node.Decode(&raw); err != nil {
			return err
		}
		var values []string
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				values = append(values, part)
			}
		}
		*s = values
		return nil
	default:
		return fmt.Errorf("expected a list or a comma-separated string")
	}
}

// UnmarshalYAML parses "minimum_interval: 24h" into a time.Duration.
// A plain YAML decoder cannot parse "24h" into time.Duration, so Policy
// decodes itself.
func (p *Policy) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		MinimumInterval string `yaml:"minimum_interval"`
		KeepLast        int    `yaml:"keep_last"`
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	if raw.MinimumInterval != "" {
		duration, err := time.ParseDuration(raw.MinimumInterval)
		if err != nil {
			return fmt.Errorf("policy.minimum_interval: %w", err)
		}
		p.MinimumInterval = duration
	}
	p.KeepLast = raw.KeepLast
	return nil
}
```

Notes:

- yaml.v3 already parses `yes` / `no` as booleans, so the old
  `legacyBooleanHook` is no longer needed.
- yaml.v3 `KnownFields(true)` gives strict decoding, which replaces
  viper's `UnmarshalExact`.

### 3b. Change the list fields to `StringList`

File: `internal/config/types.go`

In the `FileSource` struct, change:

```go
type FileSource struct {
	Include        []string `mapstructure:"include" yaml:"include"`
	Exclude        []string `mapstructure:"exclude" yaml:"exclude"`
	FollowSymlinks bool     `mapstructure:"follow_symlinks" yaml:"follow_symlinks"`
}
```

to:

```go
type FileSource struct {
	Include        StringList `mapstructure:"include" yaml:"include"`
	Exclude        StringList `mapstructure:"exclude" yaml:"exclude"`
	FollowSymlinks bool       `mapstructure:"follow_symlinks" yaml:"follow_symlinks"`
}
```

(The `mapstructure` tags are now unused but harmless — leave them.)

### 3c. Rewrite the decode path

File: `internal/config/load.go`

- Remove these imports:

  ```go
  "github.com/go-viper/mapstructure/v2"
  "github.com/spf13/viper"
  ```

- Remove `"reflect"` and `"strings"` if they were only used by the old
  hooks (the compiler will tell you).
- Add these imports:

  ```go
  "bytes"
  "gopkg.in/yaml.v3"
  ```

- Delete the functions `bindRootEnvironment`, `legacyBooleanHook`, and the
  old `decode` function.
- Add these functions:

  ```go
  func decode(path string, target any) error {
  	data, err := os.ReadFile(path)
  	if err != nil {
  		return &Error{File: path, Kind: ErrorRead, Err: err}
  	}
  	decoder := yaml.NewDecoder(bytes.NewReader(data))
  	decoder.KnownFields(true) // strict: unknown fields are an error
  	if err := decoder.Decode(target); err != nil {
  		return &Error{File: path, Kind: ErrorDecode, Err: err}
  	}
  	return nil
  }

  // applyRootEnvironment lets BQCKUP_* environment variables override the
  // root config file. Empty variables are ignored (the file wins).
  func applyRootEnvironment(app *App) {
  	if value := os.Getenv("BQCKUP_STATE_DATABASE"); value != "" {
  		app.StateDatabase = value
  	}
  	if value := os.Getenv("BQCKUP_TEMPORARY_DIRECTORY"); value != "" {
  		app.TemporaryDirectory = value
  	}
  	if value := os.Getenv("BQCKUP_LOCK_DIRECTORY"); value != "" {
  		app.LockDirectory = value
  	}
  	if value := os.Getenv("BQCKUP_LOG_LEVEL"); value != "" {
  		app.LogLevel = value
  	}
  }
  ```

- Update the three call sites in `Load`:
  - `decode(rootPath, &root, bindRootEnvironment)` →
    `decode(rootPath, &root)` followed by `applyRootEnvironment(&root.App)`
  - `decode(storagePath, &stores, nil)` → `decode(storagePath, &stores)`
  - `decode(sitePath, &doc, nil)` → `decode(sitePath, &doc)`

### 3d. Fix the `StringList` boundary conversions

`StringList` is not the same type as `[]string`. Three files pass config
lists to functions that want `[]string`. Add explicit conversions:

File: `internal/backup/runner.go` (two places: the incremental
`restic.BackupSpec` literal and the full-backup `FileSource` literal):

```go
Include: []string(site.Sources.Files.Include),
Exclude: []string(site.Sources.Files.Exclude),
```

File: `internal/engine/restic/facade/facade.go` (in `BackupFiles`, inside
the `archiver.BackupSpec` literal):

```go
Paths:    []string(spec.Include),
Excludes: []string(spec.Exclude),
```

Search for any other compile errors with:

```bash
go build ./...
```

and add the same conversion wherever the compiler reports a
`StringList`/`[]string` mismatch. Tests that construct config values with
`[]string{...}` literals keep compiling (plain literals convert
automatically).

### 3e. Clean up go.mod

```bash
go mod tidy
```

Then confirm:

```bash
grep -E "viper|mapstructure" go.mod   # must print nothing
grep "gopkg.in/yaml.v3" go.mod        # must show it as a direct dependency
```

Expect around 10 indirect dependencies to disappear (fsnotify, afero,
cast, gotenv, locafero, go-toml, sourcegraph/conc, go.yaml.in/yaml/v3,
subosito/gotenv, sagikazarmark/locafero).

### Verify Step 3

```bash
gofmt -l . && go vet ./... && go test ./... && go build ./...
go run ./cmd/bqckup config validate --config-dir ./configs
```

The smoke test must still print `valid schema v2 configuration ...`.

Also test the two special behaviors still work:

1. Comma-separated list: temporarily add `include: /a, /b` in a scratch
   config and confirm validation sees two paths.
2. Duration: a site with `minimum_interval: 1h30m` must validate; one with
   `minimum_interval: notaduration` must fail with a clear error.

---

## Step 4 — Optional cleanup (do only if all above is green)

### 4a. Simplify the history migrations

The schema has one version, and gorm's `AutoMigrate` is already idempotent,
so the `schema_migrations` table is machinery for a second migration that
does not exist yet.

- File: `internal/history/migrations.go`
  Replace the whole `Migrate` body with:

  ```go
  // Migrate brings the database schema up to date. Today that is a single
  // idempotent AutoMigrate; add a recorded version table when schema v2
  // arrives.
  func Migrate(ctx context.Context, db *gorm.DB) error {
  	if err := db.WithContext(ctx).AutoMigrate(&BackupRun{}, &Artifact{}); err != nil {
  		return fmt.Errorf("apply schema: %w", err)
  	}
  	return nil
  }
  ```

  Remove `currentSchemaVersion` and the now-unused imports.
- File: `internal/history/model.go`
  Delete the `SchemaMigration` struct.
- File: `internal/history/database_test.go`
  In `TestMigrateIsIdempotent`, drop the `schema_migrations` count
  assertion; the test then just calls `Migrate` twice and expects no error.

### 4b. Reuse one "builtin engine is local-only" check

- File: `internal/app/app.go`
  Rename `validateBuiltinEngineStorages` to `ValidateBuiltinEngineStorages`
  (exported), and update its call site in `Open`.
- File: `internal/cli/doctor.go`
  Replace the inline `localOnly` loop with a single check:

  ```go
  if err := app.ValidateBuiltinEngineStorages(cfg); err != nil {
  	addCheck("engine:builtin", "fail", apperror.UserMessage(err))
  }
  ```

  Note: this now reports one combined check instead of one per site, and it
  checks all sites even when `--site` filters. That is acceptable and
  stricter. Update `doctor_test.go` expectations if it fails.

### 4c. Merge the three artifact types

Skip this unless asked. It touches many files for little gain.

---

## Do NOT touch

- `internal/engine/restic/crypto/`, `chunker/`, `pack/builder.go`,
  `pack/pack.go`, `index/encoder.go`, `tree/tree.go` serialization,
  `snapshot/` — this is the verified restic format contract. Any "cleanup"
  here risks breaking compatibility with the real restic binary.
- Storage verification logic (sha256/size checks, `Renameat2` no-replace,
  post-upload `HeadObject` verification) — correctness, not complexity.
- Retention semantics (`keep_last`, sort order), lock behavior, exit-code
  mapping in `internal/cli/root.go`, config validation rules in
  `validate.go` (except 2e's rename).
- `internal/clock`, the `Clock` interface, and test fakes — a real test seam.
- `internal/engine/restic/compat_test.go` (build tag `restic_compat`).
- CI workflows, Makefile, docs, config examples (except running the smoke
  test against `configs/`).

---

## Definition of done

1. `make verify` passes (fmt, vet, test, build).
2. `go run ./cmd/bqckup config validate --config-dir ./configs` prints
   `valid schema v2 configuration ...`.
3. `grep -E "viper|mapstructure" go.mod` prints nothing.
4. `git diff --stat` shows a net deletion of roughly 300 lines and no
   behavior changes in config validation, backup execution, or storage.
5. All tests pass with `-race`: `go test -race ./...`.
