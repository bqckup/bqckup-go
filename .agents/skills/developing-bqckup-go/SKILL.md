---
name: developing-bqckup-go
description: Use when developing, reviewing, debugging, or planning changes in the bqckup-go repository, especially its CLI, strict YAML configuration, backup runner, storage adapters, SQLite history, database exporters, or built-in incremental engine.
---

# Developing Bqckup Go

Deliver one reviewable change while preserving Bqckup's CLI-only architecture,
strict configuration contract, and backup safety invariants.

## Start with current evidence

1. Locate the repository root. Read applicable `AGENTS.md`, then inspect the
   branch, `git status`, and recent commits. Preserve unrelated user changes.
2. Trace the requested behavior through current code and tests. Treat tests as
   executable contracts; do not rely on deleted plans or historical reports.
3. Read `README.md` and `USER-GUIDE.md` for public behavior. Check `configs/`
   and the `bqckup init` templates before changing configuration examples.
4. State the smallest observable scope and acceptance criteria. Do not combine
   unrelated features.

Read these references only when relevant:

- [architecture.md](references/architecture.md) for package boundaries,
  orchestration, storage, history, or lifecycle changes.
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
  with exact mode `0600`. Never expose passwords, access keys, endpoints,
  provider bodies, signed requests, or child environments in output or tests.
- Full archive mode and the built-in Restic-compatible incremental mode are
  both supported. Do not reintroduce Rustic, an engine selector, or an external
  Restic backup process.

## Implement and report with evidence

Write the smallest failing behavioral test, confirm the intended failure, then
implement only enough to pass. Run focused tests, `make verify`, and
`sh scripts/check-docs.sh`. Report the behavior changed, exact verification
results, docs/config impact, security and cancellation considerations, and
intentionally deferred work.
