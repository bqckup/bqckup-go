# Bqckup Go Core CLI Design

**Date:** 2026-07-23  
**Status:** Approved for implementation planning  
**Target repository:** `/home/revv/Development/backup/bqckup-go`  
**Legacy reference:** `/home/revv/Development/backup/bqckup`

## 1. Purpose

Build a maintainable Go foundation for Bqckup that interns can extend through small, testable assignments. The first milestone is a CLI-only modular monolith. It backs up file sources to local storage end to end, records execution history in SQLite, and establishes stable contracts for database exporters and S3-compatible storage.

The foundation must teach the intended engineering practices through working code, tests, documentation, and a repository-owned Codex skill. It must not reproduce the Python application's web UI or carry its configuration inconsistencies into the new codebase.

## 2. Decisions

- Use Go 1.26.
- Use Cobra for commands, flags, help, and exit behavior.
- Use Viper only inside the configuration boundary.
- Use typed configuration structs after parsing; business packages never call Viper.
- Use GORM with the official SQLite driver for internal state.
- Accept the CGO/GCC build requirement for the supported Linux target.
- Use a modular monolith with small interfaces at I/O boundaries.
- Use YAML schema version 2; do not maintain runtime compatibility with the legacy schema.
- Retain the familiar `config/` and `sites/` directory names from the Python application.
- Keep mutable state and temporary data outside the production configuration directory.
- Use archive backups for the first milestone.
- Do not port Rustic.
- Treat Restic integration as a later milestone documented by the skill, with no Restic package, dependency, command, or config field in the first milestone.
- Exclude the web UI, authentication, notifications, reporting, master API, webhook callbacks, restore, and an internal scheduler.

## 3. Legacy Findings

The Python application uses:

```text
bqckup/
├── bqckup.cnf
├── config/
│   └── storages.yml
├── sites/
│   └── <site>.yml
├── database/
│   └── bqckup.db
└── tmp/
```

The supplied runtime configuration contains four MySQL site definitions. All run daily with a retention count of seven and use an S3-compatible storage definition. Two enable Rustic incremental backup. The storage obtains credentials from a remote URL. Secrets currently appear directly in site YAML or inside a credential URL.

Schema v2 preserves the useful concepts—sites, multiple database sources, file includes/excludes, local/S3 destinations, minimum interval, and retention—but normalizes their representation. A later migration command must report unsupported legacy incremental settings instead of silently converting them to archive backups.

## 4. Scope

### 4.1 Foundation deliverable

The initial implementation provides a complete local-file vertical slice:

- application entry point and dependency wiring;
- Cobra root command and stable exit-code mapping;
- Viper loaders for the root config, storage config, and every site file;
- schema-v2 typed structs, defaults, and validation;
- `init`, `config validate`, `backup list`, `backup run`, `history list`, and `version` commands;
- GORM database opening, versioned schema migration, and repositories;
- cross-process site locking;
- file archive creation with include, exclude, and symlink policy;
- SHA-256 checksums;
- local storage with staged writes and atomic finalization;
- backup orchestration, cancellation, cleanup, history, and local retention;
- structured logging with secret redaction;
- unit, integration, and CLI tests;
- CI, build commands, example configurations, project documentation, and the repository-owned skill.

### 4.2 Intern extension milestones

The documented intern backlog contains independently reviewable milestones:

1. MySQL exporter.
2. PostgreSQL exporter.
3. SQLite source exporter.
4. S3-compatible storage adapter using AWS SDK for Go v2.
5. Environment-backed S3 credentials.
6. Remote HTTP credential provider compatible with the legacy `remote_url` use case, with the URL itself supplied through an environment variable.
7. S3 retention integration using the retention policy already implemented for local storage.
8. `doctor` dependency and connectivity checks.
9. Legacy YAML migration command.
10. Packaging and release automation.
11. Restic integration as a separate later design and implementation cycle.

Intern milestones must not be represented by production stubs that return “not implemented.” Commands and config validation expose only behavior supported by the current milestone.

## 5. Runtime Layout

Production defaults:

```text
/etc/bqckup/
├── bqckup.yaml
├── config/
│   └── storages.yaml
└── sites/
    └── <site>.yaml

/var/lib/bqckup/
├── bqckup.db
├── locks/
└── tmp/
```

The root can be overridden with `--config-dir` or `BQCKUP_CONFIG_DIR`. Development may point the state database and temporary directory into a project-local sandbox. Relative paths in `bqckup.yaml` resolve against the configuration root; relative paths in site files are rejected to avoid working-directory-dependent backups.

## 6. Configuration Schema v2

### 6.1 Root configuration

`bqckup.yaml`:

```yaml
version: 2

app:
  state_database: /var/lib/bqckup/bqckup.db
  temporary_directory: /var/lib/bqckup/tmp
  lock_directory: /var/lib/bqckup/locks
  log_level: info
```

### 6.2 Storage configuration

`config/storages.yaml`:

```yaml
version: 2

storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup

  s3-primary:
    type: s3
    bucket: company-backups
    region: ap-southeast-3
    endpoint: https://s3.example.com
    prefix: bqckup
    credentials:
      source: env
      access_key_env: BQCKUP_S3_ACCESS_KEY
      secret_key_env: BQCKUP_S3_SECRET_KEY

  s3-remote-credentials:
    type: s3
    bucket: company-backups
    region: ap-southeast-3
    endpoint: https://s3.example.com
    prefix: bqckup
    credentials:
      source: remote
      url_env: BQCKUP_S3_CREDENTIAL_URL
```

The initial local-file milestone accepts only `type: local`. S3 shapes are specified for the S3 milestones and become accepted by validation when the corresponding adapter and credential provider are delivered.

### 6.3 Site configuration

`sites/production.yaml`:

```yaml
version: 2

site:
  name: production
  enabled: true

  sources:
    files:
      include:
        - /var/www/example
      exclude:
        - /var/www/example/cache
        - /var/www/example/.git
      follow_symlinks: false

    databases:
      - name: application-mysql
        enabled: true
        engine: mysql
        host: 127.0.0.1
        port: 3306
        database: application
        username: backup
        password_env: BQCKUP_MYSQL_PASSWORD

      - name: local-sqlite
        enabled: true
        engine: sqlite
        database: /var/lib/example/application.db

  destinations:
    - storage: local-primary

  policy:
    minimum_interval: 24h
    keep_last: 7
```

The initial local-file milestone accepts file sources and a local destination. Database source shapes are specified for exporter milestones and become accepted as each exporter is delivered.

### 6.4 Parsing and precedence

Use a new Viper instance for each file. Never use Viper package-level global state.

Precedence is:

1. Cobra flag explicitly supplied by the user.
2. Environment variable prefixed with `BQCKUP_`.
3. YAML value.
4. application default.

Viper output is unmarshaled once into typed structs. Validation happens before application wiring. Configuration values are immutable during one command execution; live reload is outside scope.

### 6.5 Validation

Validation errors include the source file and field path. Validation covers:

- exact schema version `2`;
- known keys only;
- required root paths;
- unique storage and site names;
- site filename matching the declared site name after safe normalization;
- non-empty enabled source and destination lists;
- absolute file source paths;
- valid duration and positive retention count;
- storage references that exist;
- engine-specific database fields;
- environment variable names, without logging resolved values;
- feature availability for the current milestone.

Static validation does not contact databases or storage. Runtime availability belongs to `doctor` and backup preflight checks.

## 7. CLI Contract

```text
bqckup init
bqckup config validate
bqckup backup list
bqckup backup run <site> [--force]
bqckup history list [--site <name>] [--limit <n>]
bqckup version
```

`doctor` is added by its intern milestone. The CLI uses Cobra `RunE` functions and returns errors to one root-level mapper. Command packages parse input and render output but contain no backup, storage, or persistence logic.

Global flags:

```text
--config-dir <path>
--output text|json
--verbose
```

Exit codes:

| Code | Meaning |
|---:|---|
| 0 | Success, including an interval-based skip or already-running skip |
| 1 | Unexpected internal error |
| 2 | Invalid CLI input or configuration |
| 3 | Missing dependency or failed preflight |
| 4 | Backup, archive, exporter, or storage failure |

Machine-readable output never contains ANSI formatting. Human-readable errors go to stderr; normal command results go to stdout.

## 8. Architecture

```text
bqckup-go/
├── cmd/bqckup/
│   └── main.go
├── internal/
│   ├── app/                 # dependency construction and lifecycle
│   ├── cli/                 # Cobra commands and presentation
│   ├── config/              # Viper loaders, structs, defaults, validation
│   ├── backup/              # domain types and runner use case
│   │   ├── database/        # exporter contracts and later adapters
│   │   └── files/           # archive creation
│   ├── storage/             # storage contract
│   │   ├── local/           # initial implementation
│   │   └── s3/              # added only in the S3 milestone
│   ├── history/             # GORM models, migrations, repositories
│   └── platform/            # clock, filesystem, process, signal, locking
├── configs/                 # secret-free examples
├── docs/
├── migrations/
├── .agents/skills/developing-bqckup-go/
├── Makefile
├── go.mod
└── go.sum
```

Dependency direction points toward the domain and use cases. `backup.Runner` depends on interfaces for archive creation, storage, history, clock, and locking. It does not import Cobra, Viper, GORM, AWS SDK, or concrete subprocess implementations.

Do not create a public `pkg/` directory until the repository has a real external consumer.

## 9. Core Contracts

The implementation plan may refine names without changing responsibilities.

```go
type Runner interface {
    Run(ctx context.Context, site config.Site, force bool) (RunResult, error)
}

type Archiver interface {
    Create(ctx context.Context, source FileSource, destination string) (Artifact, error)
}

type Exporter interface {
    Export(ctx context.Context, database DatabaseSource, destination string) (Artifact, error)
}

type Storage interface {
    Put(ctx context.Context, artifact Artifact, objectKey string) (StoredArtifact, error)
    Delete(ctx context.Context, objectKey string) error
}

type RunRepository interface {
    Create(ctx context.Context, run *BackupRun) error
    Update(ctx context.Context, run *BackupRun) error
    List(ctx context.Context, filter RunFilter) ([]BackupRun, error)
}

type Locker interface {
    TryLock(ctx context.Context, site string) (unlock func() error, acquired bool, err error)
}
```

Interfaces live with the consumer that needs them. Avoid interface layers that have only speculative consumers.

## 10. Backup Flow

1. Parse CLI input.
2. Load and validate all configuration.
3. Resolve the requested site and destination adapters.
4. Acquire a cross-process lock keyed by normalized site name.
5. Check the most recent successful run against `minimum_interval`, unless `--force` is set.
6. Create a `running` backup-run record.
7. Create a permission-restricted temporary workspace.
8. Produce each file archive and, when exporter milestones exist, each database dump.
9. Compute SHA-256 and size for every completed artifact.
10. Put each artifact into every configured destination using a staged/finalized write.
11. Record per-destination artifact results.
12. Mark the run `success` only when every required operation succeeds.
13. Apply local retention only after success; later storage adapters reuse the same selection policy.
14. Clean temporary data and release the lock.

Cancellation through `SIGINT` or `SIGTERM` propagates through `context.Context`. External processes receive cancellation, temporary artifacts are removed, and a started run becomes `cancelled`.

Multiple destinations have all-required semantics in schema v2: failure of any configured destination fails the run. This rule is explicit and not configurable in the first milestone.

## 11. Artifact Layout

Object keys and local relative paths use a portable slash-separated form:

```text
<prefix>/<site>/<UTC timestamp>/<artifact name>
```

Example:

```text
bqckup/production/2026-07-23T03-45-00Z/files.tar.gz
bqckup/production/2026-07-23T03-45-00Z/application-mysql.sql.gz
```

Artifact names derive from normalized configured names, never from unsanitized user input. Local storage writes to a unique staging file in the target filesystem and renames it into place after the checksum and size are known.

## 12. Persistence

Initial tables:

### `schema_migrations`

- `version` integer primary key;
- `applied_at` timestamp.

### `backup_runs`

- UUID primary key;
- site name;
- status: `running`, `success`, `failed`, or `cancelled`;
- forced flag;
- started and finished timestamps;
- duration;
- error category and redacted error message;
- created and updated timestamps.

### `artifacts`

- UUID primary key;
- backup-run foreign key;
- source kind and source name;
- destination storage name;
- object key;
- size;
- SHA-256 checksum;
- status: `stored` or `failed`;
- redacted error message;
- created timestamp.

Use explicit ordered migrations recorded in `schema_migrations`. A migration may use GORM's migrator, but application commands do not call unrestricted `AutoMigrate`.

SQLite settings:

- WAL journal mode;
- foreign keys enabled;
- busy timeout configured;
- one open writer connection;
- database and parent directory created with restrictive permissions.

## 13. Error Handling and Security

- Return errors; reserve panic for impossible programmer invariants during startup.
- Wrap causal errors with `%w`.
- Map errors to config, preflight, execution, storage, persistence, cancellation, or internal categories.
- Keep one CLI boundary responsible for exit codes and user rendering.
- Use `exec.CommandContext` with argument slices; never build shell command strings.
- Pass database passwords through the child environment where supported and redact command arguments where a legacy tool requires a password flag.
- Never log password values, access keys, secret keys, credential URLs, webhook URLs, child environments, or full config dumps.
- Create temporary directories and files with owner-only permissions.
- Reject archive paths that would escape the intended archive root.
- Define symlink behavior explicitly and test it.
- Do not delete previously successful backups after a current-run failure.
- Apply retention against known Bqckup prefixes only.
- Ensure JSON output contains stable categories without exposing internal stack traces.

## 14. Testing and Verification

All feature and bug-fix work follows red-green-refactor. Tests focus on observable behavior and use real code unless an external boundary makes a fake necessary.

Test layers:

- unit tests for config parsing, defaults, validation, normalization, interval decisions, runner state transitions, key generation, error mapping, and retention selection;
- contract tests reused by storage and exporter implementations;
- integration tests with temporary SQLite, real filesystem archives, local storage, migrations, cancellation, and cleanup;
- CLI tests by constructing Cobra commands with injected dependencies and in-memory stdout/stderr;
- fake process executors that verify executable name, arguments, environment keys, cancellation, and exit handling without contacting production databases;
- end-to-end local-file backup tests in a temporary directory.

Required verification commands:

```text
gofmt check
go vet ./...
go test -race ./...
go build ./cmd/bqckup
```

CI must run on pull requests. Tests must not depend on production config, real credentials, a network connection, or fixed host paths.

## 15. Documentation

Repository documentation:

```text
README.md
docs/
├── architecture.md
├── configuration-v2.md
├── development.md
├── testing.md
├── migration-from-python.md
└── intern-backlog.md
```

`README.md` provides project purpose, milestone status, quick start, commands, and links. Detailed rules live in one canonical document and are referenced rather than duplicated.

`intern-backlog.md` defines each milestone with prerequisites, permitted scope, acceptance criteria, test expectations, and review checklist. Each assignment must produce independently testable behavior and must not require an intern to infer an unpublished interface.

## 16. Codex Skill

Canonical repository location:

```text
.agents/skills/developing-bqckup-go/
├── SKILL.md
├── agents/
│   └── openai.yaml
└── references/
    ├── architecture.md
    ├── config-v2.md
    ├── contribution-workflow.md
    └── restic-roadmap.md
```

The skill triggers when implementing, reviewing, planning, or debugging work in `bqckup-go`. It instructs an agent to:

- inspect repository state and applicable docs before editing;
- identify the selected milestone and keep changes within it;
- maintain dependency direction and typed config boundaries;
- use test-driven development and verification;
- preserve secret redaction and safe subprocess execution;
- update canonical docs when a public contract changes;
- reject web UI and Rustic work as out of scope;
- treat Restic as a separately approved later milestone.

Detailed project facts belong in references or repository docs, not in an oversized `SKILL.md`.

The repository copy is authoritative. The global installation at `~/.codex/skills/developing-bqckup-go` is a symlink to the repository skill on the maintainer's workstation so both copies remain identical. Other contributors use the committed repository skill or install it into their own global skills directory.

The skill is developed and validated with a baseline/application cycle:

1. Run representative repository tasks without the skill and record gaps.
2. Write the smallest guidance that addresses observed gaps.
3. Run the same tasks with the skill.
4. Refine guidance only for observed ambiguity or failure.
5. Run the skill validator and verify `agents/openai.yaml` matches `SKILL.md`.

## 17. Restic Roadmap

The skill reference `restic-roadmap.md` records constraints, not implementation instructions masquerading as completed behavior:

- start Restic work with a separate design review;
- confirm backup, restore, retention, encryption, repository initialization, locking, and credential semantics against current Restic documentation;
- add Restic through an adapter boundary rather than conditional logic spread through the runner;
- keep archive mode working and backwards compatible;
- never store repository passwords directly in site YAML;
- add configuration only when the Restic milestone implements and tests it;
- include safe restore behavior and explicit overwrite confirmation in the Restic milestone.

## 18. Acceptance Criteria

The foundation is ready for intern assignments when:

- a clean checkout builds on the documented Linux toolchain;
- example schema-v2 local configuration validates;
- invalid configuration reports source file and field path;
- `backup run <site>` archives configured files into local storage end to end;
- a completed run and its artifact metadata appear in `history list`;
- local retention preserves exactly the configured number of successful backup sets;
- interval skip, force, already-running lock, cancellation, failure, and cleanup behaviors are tested;
- SQLite migrations are deterministic on an empty and already-initialized database;
- logs and CLI output pass secret-redaction tests;
- all required verification commands pass;
- documentation and intern backlog match implemented behavior;
- the repository skill passes validation and its representative forward tests;
- the global skill resolves to the committed repository skill;
- web UI, Rustic, Restic, notifications, and restore code are absent from the foundation.
