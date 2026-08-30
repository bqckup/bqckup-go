---
name: developing-bqckup-go
description: Use when developing, reviewing, debugging, or planning changes in the bqckup-go repository, especially its CLI, strict YAML configuration, backup and restore orchestration, notifications, retention, storage adapters, SQLite history, database exporters, doctor checks, or built-in incremental engine.
---

# Developing Bqckup Go

Deliver one reviewable change while preserving Bqckup's CLI-only architecture,
strict configuration contract, and backup safety invariants.

## Start with current evidence

1. Locate the repository root. Read applicable `AGENTS.md`, then inspect the
   branch, `git status`, and recent commits. Preserve unrelated user changes.
2. Trace the requested behavior through current code and tests. Treat tests as
   executable contracts; do not rely on deleted plans or historical reports.
3. Read `README.md`, `USER-GUIDE.md`, and the current changelog entry for
   public behavior. Check `configs/`, `bqckup --help`, and the `bqckup init`
   templates before changing commands or configuration examples.
4. State the smallest observable scope and acceptance criteria. Do not combine
   unrelated features.

Read these references only when relevant:

- [architecture.md](references/architecture.md) for package boundaries,
  orchestration, storage, history, notifications, doctor, or lifecycle changes.
- [config.md](references/config.md) for YAML, defaults, validation,
  credentials, or initialization templates.
- [incremental-engine.md](references/incremental-engine.md) for incremental
  repositories, compatibility, locking, retention, or restore behavior.
- [contribution-workflow.md](references/contribution-workflow.md) before
  implementation and handoff.

## Preserve runtime contracts

- Keep Cobra in `internal/cli`, Viper in `internal/config`, GORM in
  `internal/history`, orchestration in `internal/backup`, and concrete wiring
  in `internal/app`.
- Every produced artifact must reach every configured destination. Apply
  retention only after all required work succeeds.
- Pass `context.Context` through I/O. Cancellation must stop subprocesses and
  remove incomplete temporary output.
- Use `exec.CommandContext` with explicit arguments. Database passwords are
  read from protected site YAML and passed to exporters through `MYSQL_PWD` or
  `PGPASSWORD`, never command arguments.
- Credential-bearing site and storage YAML must be regular non-symlink files
  with exact mode `0600`; the same applies to a credential-bearing root YAML.
  YAML values are authoritative: do not reinterpret config fields as
  environment-variable names or add environment overrides for their values.
  `BQCKUP_CONFIG_DIR` may only select the config directory. Never expose
  passwords, access keys, webhook URLs, endpoints, provider bodies, signed
  requests, or child environments in output or tests.
- Namespace full artifacts under `bqckup/<server_id>/<site>/<UTC timestamp>/`
  and incremental repositories under
  `bqckup/<server_id>/<site>/incremental-backup/`. Preserve the tested legacy
  fallback only when `server_id` is empty.
- Record terminal history before best-effort notifications. Notification
  failures warn but never alter backup status or history; `events: [all]`
  matches every supported terminal notification event.
- Full archive mode and the built-in Restic-compatible incremental mode are
  both supported. Do not reintroduce Rustic, an engine selector, or an external
  Restic backup process.
- Restore only incremental snapshots, require an explicit destination and
  target, preserve safe no-overwrite confirmation, and never write restore
  operations into backup history.

## Implement and report with evidence

Write the smallest failing behavioral test, confirm the intended failure, then
implement only enough to pass. Default tests must never contact production
storage, providers, SMTP, or webhooks. Run focused tests, `make verify`, and
`sh scripts/check-docs.sh`. Report the behavior changed, exact verification
results, docs/config impact, security and cancellation considerations, and
intentionally deferred work.
