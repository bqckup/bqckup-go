# Bqckup Go Core CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a tested Go CLI foundation that validates schema-v2 configuration, performs local file backups, records history in SQLite, applies local retention, and provides repository documentation plus a reusable Codex skill.

**Architecture:** Implement a modular monolith under `internal/`. Cobra and Viper stay at input boundaries, GORM stays behind history repositories, and `backup.Runner` coordinates interfaces for archiving, storage, locking, time, and persistence. Deliver one end-to-end local-file vertical slice before exposing extension points for database exporters and S3.

**Tech Stack:** Go 1.26 toolchain, Cobra, Viper, GORM, official GORM SQLite driver, `golang.org/x/sys/unix`, standard-library `archive/tar`, `compress/gzip`, `crypto/sha256`, `log/slog`, GitHub Actions, python-docx for the mentor checklist.

## Global Constraints

- Repository module path is `github.com/bqckup/bqckup-go`.
- Target Go toolchain is Go 1.26; CGO and GCC are required by the official GORM SQLite driver.
- No web UI, authentication, notifications, reporting, webhook, restore, Rustic, or Restic code.
- Viper is used only in `internal/config`; use one instance per YAML file and strict typed unmarshalling.
- Runtime secrets are referenced by environment-variable name and never logged.
- Production code is written only after a test fails for the intended reason.
- No production stubs that return an unimplemented error.
- The committed repository skill is authoritative; the global skill is a symlink to it.

---

### Task 1: Bootstrap the module and a testable Cobra entry point

**Files:**
- Create: `go.mod`
- Create: `cmd/bqckup/main.go`
- Create: `internal/cli/root.go`
- Create: `internal/cli/root_test.go`
- Create: `internal/buildinfo/buildinfo.go`
- Create: `Makefile`
- Modify: `README.md`

**Interfaces:**
- Produces: `cli.NewRoot(buildinfo.Info) *cobra.Command`
- Produces: `buildinfo.Current() Info`
- Produces: process exit through `cli.Execute(ctx, stdout, stderr) int`

- [ ] **Step 1: Initialize the module and pin CLI dependencies**

Run:

```bash
go mod init github.com/bqckup/bqckup-go
go mod edit -go=1.26 -toolchain=go1.26.5
go get github.com/spf13/cobra@latest github.com/spf13/viper@latest github.com/stretchr/testify@latest
```

Expected: `go.mod` declares the module and Go 1.26 toolchain.

- [ ] **Step 2: Write failing root/version tests**

```go
func TestVersionCommandWritesStableText(t *testing.T) {
    root := NewRoot(buildinfo.Info{Version: "0.1.0", Commit: "abc123"})
    out := new(bytes.Buffer)
    root.SetOut(out)
    root.SetErr(out)
    root.SetArgs([]string{"version"})

    require.NoError(t, root.Execute())
    assert.Equal(t, "bqckup 0.1.0 (abc123)\n", out.String())
}

func TestRootRejectsUnknownCommand(t *testing.T) {
    root := NewRoot(buildinfo.Info{Version: "dev", Commit: "unknown"})
    root.SetArgs([]string{"missing"})
    assert.Error(t, root.Execute())
}
```

- [ ] **Step 3: Run the tests and verify the missing API failure**

Run: `go test ./internal/cli -run 'TestVersionCommand|TestRootRejects' -v`

Expected: FAIL because `NewRoot` does not exist.

- [ ] **Step 4: Implement the root command and build information**

Implement `buildinfo.Info`, `buildinfo.Current`, `cli.NewRoot`, and `cli.Execute`. Use Cobra `RunE`, set `SilenceUsage: true`, route output through command writers, and map success to exit code `0` and an unclassified root error to `1`. `main.go` creates a signal-aware context and calls `os.Exit` exactly once.

- [ ] **Step 5: Add deterministic developer commands**

```make
.PHONY: fmt vet test build verify

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

vet:
	go vet ./...

test:
	go test -race ./...

build:
	go build ./cmd/bqckup

verify: fmt vet test build
```

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/cli -v && go build ./cmd/bqckup`

Expected: PASS and a successful build.

```bash
git add go.mod go.sum cmd internal/cli internal/buildinfo Makefile README.md
git commit -m "feat: bootstrap bqckup Go CLI"
```

### Task 2: Load and strictly validate schema-v2 configuration

**Files:**
- Create: `internal/config/types.go`
- Create: `internal/config/load.go`
- Create: `internal/config/validate.go`
- Create: `internal/config/errors.go`
- Create: `internal/config/load_test.go`
- Create: `internal/config/validate_test.go`
- Create: `configs/bqckup.yaml`
- Create: `configs/config/storages.yaml`
- Create: `configs/sites/example.yaml`

**Interfaces:**
- Produces: `config.Load(ctx context.Context, dir string) (Config, error)`
- Produces: `config.Config.Validate() error`
- Produces: `config.Config.Site(name string) (Site, bool)`

- [ ] **Step 1: Write failing strict-load tests**

Create fixtures inside `t.TempDir()` and verify:

```go
func TestLoadRejectsUnknownFieldWithFileAndPath(t *testing.T) {
    dir := writeConfigTree(t, `version: 2
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
  mystery: true
`, localStorageYAML, localSiteYAML)

    _, err := Load(context.Background(), dir)
    require.Error(t, err)
    assert.Contains(t, err.Error(), "bqckup.yaml")
    assert.Contains(t, err.Error(), "mystery")
}

func TestLoadUsesOneSitePerFileAndResolvesRootPaths(t *testing.T) {
    dir := writeValidConfigTree(t)
    cfg, err := Load(context.Background(), dir)
    require.NoError(t, err)
    assert.Len(t, cfg.Sites, 1)
    assert.Equal(t, filepath.Join(dir, "data/bqckup.db"), cfg.App.StateDatabase)
}
```

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/config -run 'TestLoad' -v`

Expected: FAIL because `Load` and typed structs do not exist.

- [ ] **Step 3: Define exact config structs**

Define `Config`, `App`, `Storage`, `CredentialConfig`, `Site`, `Sources`, `FileSource`, `DatabaseSource`, `Destination`, and `Policy` with `mapstructure` and `yaml` tags. Use `time.Duration` through a decode hook. Represent storages as `map[string]Storage` and sites as `[]Site`.

- [ ] **Step 4: Implement Viper boundary loading**

For each file, create `viper.New()`, set the exact file path and YAML type, call `ReadInConfig`, then `UnmarshalExact`. Bind explicit root environment overrides for state, temporary, lock, and log level. Wrap each error in `config.Error{File, Field, Kind, Err}`. Load `sites/*.yaml` in sorted filename order.

- [ ] **Step 5: Write failing validation table tests**

Cover exact version `2`, site filename/name match, unique site names, absolute source paths, non-empty include/destination, known storage reference, positive `keep_last`, positive `minimum_interval`, supported `local` storage, and milestone rejection of database sources/S3.

```go
func TestValidateRejectsRelativeSourcePath(t *testing.T) {
    cfg := validConfig()
    cfg.Sites[0].Sources.Files.Include = []string{"relative/path"}
    err := cfg.Validate()
    require.Error(t, err)
    assert.Contains(t, err.Error(), "sites.example.sources.files.include[0]")
}
```

- [ ] **Step 6: Implement validation and example configs**

Implement small `Validate` methods per type. Normalize names with lowercase ASCII letters, digits, dots, dashes, and underscores. Never resolve secret values during static validation. Example configs must contain no real domain, user, bucket, endpoint, or credential.

- [ ] **Step 7: Verify and commit**

Run: `go test ./internal/config -v`

Expected: PASS.

```bash
git add internal/config configs
git commit -m "feat: add schema v2 configuration"
```

### Task 3: Add versioned GORM SQLite history persistence

**Files:**
- Create: `internal/history/model.go`
- Create: `internal/history/database.go`
- Create: `internal/history/migrations.go`
- Create: `internal/history/repository.go`
- Create: `internal/history/database_test.go`
- Create: `internal/history/repository_test.go`

**Interfaces:**
- Produces: `history.Open(path string) (*gorm.DB, func() error, error)`
- Produces: `history.Migrate(ctx context.Context, db *gorm.DB) error`
- Produces: `history.Repository` methods `CreateRun`, `FinishRun`, `CreateArtifact`, `LastSuccessful`, and `ListRuns`

- [ ] **Step 1: Add persistence dependencies**

Run:

```bash
go get gorm.io/gorm@latest gorm.io/driver/sqlite@latest github.com/google/uuid@latest
```

- [ ] **Step 2: Write failing migration tests**

```go
func TestMigrateIsIdempotent(t *testing.T) {
    db, closeDB, err := Open(filepath.Join(t.TempDir(), "state.db"))
    require.NoError(t, err)
    t.Cleanup(func() { require.NoError(t, closeDB()) })

    require.NoError(t, Migrate(context.Background(), db))
    require.NoError(t, Migrate(context.Background(), db))

    var count int64
    require.NoError(t, db.Table("schema_migrations").Count(&count).Error)
    assert.EqualValues(t, 1, count)
}
```

- [ ] **Step 3: Run migration test to verify RED**

Run: `go test ./internal/history -run TestMigrateIsIdempotent -v`

Expected: FAIL because `Open` and `Migrate` do not exist.

- [ ] **Step 4: Implement database opening and migration 1**

Open SQLite with WAL, foreign keys, and a 5000 ms busy timeout in the DSN. Create the parent directory as `0700`, set the database file to `0600`, and configure one open connection. Migration 1 creates `backup_runs` and `artifacts` with a foreign key and records version `1` in `schema_migrations` inside a transaction.

- [ ] **Step 5: Write repository lifecycle tests**

Verify creating a running record, finishing it as success/failure/cancelled, writing per-destination artifacts, querying the most recent successful run, limiting history, and preserving redacted error fields.

- [ ] **Step 6: Implement repository methods with context**

Every GORM call uses `db.WithContext(ctx)`. Repository methods return wrapped errors and never log models or SQL values containing error details.

- [ ] **Step 7: Verify and commit**

Run: `go test ./internal/history -v`

Expected: PASS.

```bash
git add internal/history go.mod go.sum
git commit -m "feat: add SQLite backup history"
```

### Task 4: Implement file archiving and cross-process locking

**Files:**
- Create: `internal/backup/types.go`
- Create: `internal/backup/files/archiver.go`
- Create: `internal/backup/files/archiver_test.go`
- Create: `internal/platform/lock/flock_linux.go`
- Create: `internal/platform/lock/flock_test.go`

**Interfaces:**
- Produces: `files.Archiver.Create(ctx, source, destination) (backup.Artifact, error)`
- Produces: `lock.New(directory string) *Locker`
- Produces: `Locker.TryLock(ctx, site) (unlock func() error, acquired bool, err error)`

- [ ] **Step 1: Add the Linux locking dependency**

Run: `go get golang.org/x/sys@latest`

- [ ] **Step 2: Write failing archive behavior tests**

Test two source roots, nested files, absolute exclude prefixes, a stored symlink when `follow_symlinks=false`, cancellation, deterministic archive member names, SHA-256, size, and cleanup of an incomplete destination.

```go
func TestCreateExcludesConfiguredSubtree(t *testing.T) {
    source := makeSourceTree(t, map[string]string{
        "keep/a.txt": "keep",
        "cache/b.txt": "drop",
    })
    out := filepath.Join(t.TempDir(), "files.tar.gz")
    artifact, err := New().Create(context.Background(), backup.FileSource{
        Include: []string{source},
        Exclude: []string{filepath.Join(source, "cache")},
    }, out)
    require.NoError(t, err)
    assert.Equal(t, []string{"source/keep/a.txt"}, archiveMembers(t, out))
    assert.NotEmpty(t, artifact.SHA256)
}
```

- [ ] **Step 3: Implement a safe tar+gzip archiver**

Use `filepath.WalkDir`, check `ctx.Err()` for every entry, normalize archive names with `path.Join`, reject `..` escape, and create output as `0600`. Never use shell commands. Store symlink entries with `os.Readlink` when not following them. When following symlinks, resolve targets, prevent directory cycles with a canonical-path set, and archive contents under the symlink's logical archive path.

- [ ] **Step 4: Write and implement lock contention tests**

Acquire the same normalized site twice; the first call returns `acquired=true`, the second returns `false` without blocking, and a third succeeds after unlock. Implement with `unix.Flock(LOCK_EX|LOCK_NB)` and owner-only lock files.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/backup/files ./internal/platform/lock -v`

Expected: PASS.

```bash
git add internal/backup internal/platform go.mod go.sum
git commit -m "feat: add safe file archiving and locking"
```

### Task 5: Implement atomic local storage and local retention

**Files:**
- Create: `internal/storage/storage.go`
- Create: `internal/storage/local/local.go`
- Create: `internal/storage/local/local_test.go`
- Create: `internal/retention/policy.go`
- Create: `internal/retention/policy_test.go`

**Interfaces:**
- Produces: `storage.Store.Put(ctx, artifact, key) (storage.StoredArtifact, error)`
- Produces: `storage.Store.Delete(ctx, key) error`
- Produces: `storage.Store.ListBackupSets(ctx, sitePrefix) ([]storage.BackupSet, error)`
- Produces: `retention.Apply(ctx, store, sitePrefix, keepLast) error`

- [ ] **Step 1: Write failing path-safety and atomicity tests**

Verify `../escape` and absolute keys are rejected, cancellation removes staging files, existing final objects are not overwritten, and successful writes preserve checksum and owner-readable permissions.

- [ ] **Step 2: Implement local storage**

Resolve final paths beneath the configured root, copy into a unique staging file in the final filesystem, `Sync`, close, verify checksum, then rename. `ListBackupSets` recognizes only `<prefix>/<site>/<UTC timestamp>/` directories matching the application timestamp parser.

- [ ] **Step 3: Write failing retention tests**

```go
func TestApplyKeepsNewestSuccessfulSets(t *testing.T) {
    store := newFakeStore([]string{"2026-01-01T00-00-00Z", "2026-01-02T00-00-00Z", "2026-01-03T00-00-00Z"})
    require.NoError(t, Apply(context.Background(), store, "bqckup/site", 2))
    assert.Equal(t, []string{"2026-01-01T00-00-00Z"}, store.deleted)
}
```

- [ ] **Step 4: Implement retention selection**

Sort validated backup sets oldest first and delete only the excess. Reject `keepLast < 1`. Stop at the first deletion error and return a categorized storage error.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/storage/... ./internal/retention -v`

Expected: PASS.

```bash
git add internal/storage internal/retention
git commit -m "feat: add local storage and retention"
```

### Task 6: Implement the backup runner lifecycle

**Files:**
- Create: `internal/backup/runner.go`
- Create: `internal/backup/runner_test.go`
- Create: `internal/apperror/error.go`
- Create: `internal/clock/clock.go`

**Interfaces:**
- Consumes: archiver, store resolver, run repository, retainer, locker, and clock contracts
- Produces: `backup.NewRunner(Dependencies) *Runner`
- Produces: `Runner.Run(ctx, site, force) (RunResult, error)`

- [ ] **Step 1: Write runner state-transition tests**

Cover success, interval skip, forced run, already-running skip, archive failure, storage failure, cancellation, cleanup, multiple destinations with all-required semantics, artifact persistence, and retention only after success.

```go
func TestRunnerDoesNotApplyRetentionAfterStorageFailure(t *testing.T) {
    deps := successfulDependencies()
    deps.Stores["local"].PutError = errors.New("disk full")
    result, err := NewRunner(deps).Run(context.Background(), validSite(), false)
    require.Error(t, err)
    assert.Equal(t, StatusFailed, result.Status)
    assert.Equal(t, 0, deps.Retainer.Calls)
    assert.Equal(t, StatusFailed, deps.Repository.LastRun.Status)
}
```

- [ ] **Step 2: Run runner tests to verify RED**

Run: `go test ./internal/backup -run TestRunner -v`

Expected: FAIL because `NewRunner` and `Run` do not exist.

- [ ] **Step 3: Implement the lifecycle in one orchestration unit**

Keep the runner free from Cobra, Viper, GORM, and concrete filesystem types. Use a single deferred cleanup stack. Persist `running` before I/O, persist terminal status exactly once, use UTC timestamps, create object keys through one sanitizer, and redact causal messages before repository writes.

- [ ] **Step 4: Add stable error categories and exit mapping inputs**

Define config, preflight, execution, storage, persistence, cancellation, and internal categories in `internal/apperror`. Preserve wrapped causes for `errors.Is/As`; expose only category and redacted user message to CLI output.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/backup ./internal/apperror -v`

Expected: PASS.

```bash
git add internal/backup internal/apperror internal/clock
git commit -m "feat: orchestrate local backup runs"
```

### Task 7: Wire application services and complete the CLI commands

**Files:**
- Create: `internal/app/app.go`
- Create: `internal/app/app_test.go`
- Create: `internal/cli/init.go`
- Create: `internal/cli/config.go`
- Create: `internal/cli/backup.go`
- Create: `internal/cli/history.go`
- Create: `internal/cli/commands_test.go`
- Modify: `internal/cli/root.go`
- Modify: `cmd/bqckup/main.go`

**Interfaces:**
- Produces: `app.Open(ctx, configDir) (*App, error)` and `App.Close() error`
- Produces commands from the approved CLI contract

- [ ] **Step 1: Write failing command tests**

Use injected application factories and buffers to test safe `init`, config validation output, sorted backup listing, backup success/skip/failure, history limit/filter, JSON output without ANSI, stderr routing, and exit codes `0` through `4`.

- [ ] **Step 2: Implement application wiring**

`app.Open` loads config, opens/migrates SQLite, constructs repositories, lock manager, archiver, local stores, retainer, and runner. Close functions execute in reverse construction order and join errors.

- [ ] **Step 3: Implement commands without business logic**

`init` creates a schema-v2 local example tree only when files do not exist. `config validate` reports counts and file locations. `backup list` shows site, enabled state, sources, destinations, and last success. `backup run` invokes the runner. `history list` uses repository filters. Support `text` and `json` renderers.

- [ ] **Step 4: Add a real temporary-directory end-to-end test**

Create config and a source tree, run the Cobra command, then assert a `.tar.gz` exists under the expected UTC backup set and `history list --output json` returns one successful run with one stored artifact.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/app ./internal/cli -v && go run ./cmd/bqckup version`

Expected: PASS and stable version output.

```bash
git add internal/app internal/cli cmd/bqckup
git commit -m "feat: complete core CLI workflow"
```

### Task 8: Add CI and canonical project documentation

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `docs/architecture.md`
- Create: `docs/configuration-v2.md`
- Create: `docs/development.md`
- Create: `docs/testing.md`
- Create: `docs/migration-from-python.md`
- Create: `docs/intern-backlog.md`
- Modify: `README.md`

**Interfaces:**
- Produces: an onboarding path and independently assignable intern milestones

- [ ] **Step 1: Write a documentation contract check**

Add `scripts/check-docs.sh` that verifies required files, approved command names, `version: 2` examples, all eleven intern milestones, explicit exclusion of Rustic/Restic from the foundation, and absence of real supplied domains or credential values.

- [ ] **Step 2: Run the check to verify RED**

Run: `sh scripts/check-docs.sh`

Expected: FAIL listing missing documentation files.

- [ ] **Step 3: Write the canonical documentation**

Keep architecture and config contracts aligned with the approved spec. Each intern backlog entry contains objective, prerequisites, in-scope files, out-of-scope behavior, acceptance checks, required tests, and suggested commit title. Mark Restic as a later design cycle, not a current implementation assignment.

- [ ] **Step 4: Add CI**

Use `actions/setup-go` with Go 1.26, install GCC on Ubuntu, cache modules, and run `make verify` plus `sh scripts/check-docs.sh`.

- [ ] **Step 5: Verify and commit**

Run: `sh scripts/check-docs.sh && make verify`

Expected: PASS.

```bash
git add README.md docs .github scripts
git commit -m "docs: add intern-ready project guide"
```

### Task 9: Create and validate the `developing-bqckup-go` skill

**Files:**
- Create: `.agents/skills/developing-bqckup-go/SKILL.md`
- Create: `.agents/skills/developing-bqckup-go/agents/openai.yaml`
- Create: `.agents/skills/developing-bqckup-go/references/architecture.md`
- Create: `.agents/skills/developing-bqckup-go/references/config-v2.md`
- Create: `.agents/skills/developing-bqckup-go/references/contribution-workflow.md`
- Create: `.agents/skills/developing-bqckup-go/references/restic-roadmap.md`
- Create: `/home/revv/Documents/Codex/2026-07-23/jad/work/skill-tests/baseline.md`
- Create: `/home/revv/Documents/Codex/2026-07-23/jad/work/skill-tests/with-skill.md`

**Interfaces:**
- Produces: repository skill source and global symlink at `~/.codex/skills/developing-bqckup-go`

- [ ] **Step 1: Run baseline application scenarios without the skill**

Use fresh agents on: adding a MySQL exporter, changing YAML schema, and starting Restic work prematurely. Record whether they violate package boundaries, skip tests, expose secrets, or implement out-of-scope behavior.

- [ ] **Step 2: Initialize the skill with official tooling**

Run `init_skill.py developing-bqckup-go` into `.agents/skills` with `references` resources and explicit UI metadata. Do not hand-create an alternate skill layout.

- [ ] **Step 3: Write the minimal skill and references**

`SKILL.md` contains the trigger-focused workflow and remains concise. References point to canonical repository docs and add only decision guidance needed during execution. `restic-roadmap.md` requires a separate approved design and current official Restic documentation before any Restic code or config change.

- [ ] **Step 4: Run the same scenarios with the skill**

Record outputs and verify agents select one intern milestone, use TDD, preserve boundaries, redact secrets, and defer Restic. Refine only observed failures.

- [ ] **Step 5: Validate metadata and skill structure**

Run `quick_validate.py .agents/skills/developing-bqckup-go` and regenerate `agents/openai.yaml` if it does not match `SKILL.md`.

- [ ] **Step 6: Install the global symlink and commit**

Resolve both paths before creating the link. Refuse to replace an unrelated existing global skill. Link `~/.codex/skills/developing-bqckup-go` to the committed repository directory, then verify `readlink -f` resolves to the repository skill.

```bash
git add .agents/skills/developing-bqckup-go
git commit -m "feat: add bqckup Go development skill"
```

### Task 10: Create the mentor feature-checklist DOCX

**Files:**
- Create: `work/build_feature_checklist.py`
- Create: `outputs/Bqckup-Go-Intern-Feature-Checklist.docx`
- Create: `work/docx-render/` (QA only)

**Interfaces:**
- Produces: a Word checklist matching `docs/intern-backlog.md`

- [ ] **Step 1: Define document content and design tokens**

Use the `compact_reference_guide` preset with Letter portrait, one-inch margins, Calibri 11 pt, blue heading hierarchy, 9360-DXA fixed tables, 120-DXA table indent, and explicit cell padding. Use a `memo_masthead` opening without a bottom border. Include metadata fields for project, mentor, intern, start date, target date, and branch/issue.

- [ ] **Step 2: Build genuine checklist tables**

Create sections for Foundation Status and Intern Feature Assignments. Each feature row has checkbox, feature, scope/deliverable, acceptance evidence, assignee, target date, PR, and mentor sign-off. Use multiple readable tables by milestone group rather than one cramped eight-column table. Include a final release-readiness checklist and notes area.

- [ ] **Step 3: Run structural audits**

Verify required feature names, table headers, repeated header rows, exact table geometry, no placeholder tokens, no credential/domain values, and matching count with `docs/intern-backlog.md`.

- [ ] **Step 4: Render and inspect every page**

Run the bundled `render_docx.py` with the workspace Python runtime. Open every page PNG at 100% zoom. Fix clipping, awkward wraps, boundary-hugging text, page breaks, and table alignment, then re-render until clean.

- [ ] **Step 5: Save only the final DOCX as the user-facing artifact**

Keep renderer PNG/PDF files in `work/docx-render`; link only `outputs/Bqckup-Go-Intern-Feature-Checklist.docx` in the final response.

### Task 11: Full verification and release-ready handoff

**Files:**
- Modify only files needed to fix verification defects

**Interfaces:**
- Produces: verified repository, clean worktree, committed foundation, and unpushed commits ready for GitHub review

- [ ] **Step 1: Run all repository checks from a clean shell**

Run:

```bash
make verify
sh scripts/check-docs.sh
go run ./cmd/bqckup version
git diff --check
git status --short
```

Expected: all commands pass and only intentional output artifacts outside the repository remain untracked.

- [ ] **Step 2: Verify the global skill target**

Run: `readlink -f ~/.codex/skills/developing-bqckup-go`

Expected: repository `.agents/skills/developing-bqckup-go` directory.

- [ ] **Step 3: Re-render the final DOCX after any content change**

Expected: every page passes visual inspection and the DOCX matches the final intern backlog.

- [ ] **Step 4: Commit verification fixes separately**

```bash
git add cmd internal configs docs .github scripts Makefile go.mod go.sum README.md .agents/skills/developing-bqckup-go
git commit -m "chore: finalize core CLI foundation"
```

- [ ] **Step 5: Prepare handoff**

Report commit range, verification evidence, feature checklist link, global skill location, and the fact that pushing to GitHub remains an external publication step.
