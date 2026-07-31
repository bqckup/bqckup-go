---
name: developing-bqckup-go
description: Develop, review, debug, or plan changes in the bqckup-go repository. Use for Go CLI commands, schema-v2 YAML, backup runners, file or database exporters, local/S3 storage, GORM SQLite history, migration, tests, intern milestones, and the later Restic design cycle.
---

# Developing Bqckup Go

Build one reviewable vertical slice while preserving the repository's CLI-only architecture, strict configuration, and secret-safety rules.

## Start every task

1. Locate the repository root and inspect `git status`, the current branch, and applicable `AGENTS.md` instructions.
2. Read the canonical repository documents relevant to the request:
   - `docs/architecture.md`
   - `docs/configuration-v2.md` for any config or adapter change
   - `docs/intern-backlog.md` for the selected milestone
   - `docs/development.md` and `docs/testing.md` before implementation
3. State the selected milestone and keep the change inside it. If prerequisites are missing or the request spans milestones, stop and surface the dependency instead of silently broadening scope.
4. Inspect existing contracts and tests before proposing new abstractions.

Use [architecture.md](references/architecture.md) for dependency decisions and [contribution-workflow.md](references/contribution-workflow.md) for the implementation checklist. Read [config-v2.md](references/config-v2.md) whenever YAML, defaults, environment variables, or validation change.

## Implement with evidence

1. Define observable acceptance criteria and the smallest focused test command.
2. Add a failing test and confirm it fails for the intended missing behavior.
3. Implement the smallest coherent vertical slice; do not add production stubs or expose unfinished config/commands.
4. Keep interfaces consumer-owned and concrete construction in `internal/app`.
5. Update examples and canonical docs in the same change as a public CLI, config, persistence, or adapter contract.
6. Run focused tests, then `make verify` and `sh scripts/check-docs.sh`.

## Preserve boundaries

- Keep Cobra in `internal/cli`, Viper in `internal/config`, GORM in `internal/history`, orchestration in `internal/backup`, and concrete wiring in `internal/app`.
- Domain/use-case code must not import Cobra, Viper, GORM, AWS SDK clients, or concrete process implementations.
- Pass `context.Context` through I/O; cancellation must stop subprocesses and remove incomplete output.
- Use `exec.CommandContext` with explicit argument slices. Never build a shell command.
- Store only environment-variable names in YAML. Never expose secret values, provider URLs, child environments, or response bodies in logs, CLI output, JSON, history, fixtures, or arguments.
- Multiple destinations are all-required. Never apply retention after a failed export or storage operation.
- Preserve archive mode and SQLite migration ordering unless the selected milestone explicitly changes their contracts.

## Scope gates

- Web UI, authentication, notifications, reporting, webhook, internal scheduling, and Rustic are out of scope.
- S3, credential providers, exporters, doctor, migration, and packaging are separate milestones; do not combine them for convenience.
- Restic is design-only until a separate proposal is approved. Before any Restic work, read [restic-roadmap.md](references/restic-roadmap.md). Do not add a dependency, package, command, or config field during the design cycle.

## Review output

Report the milestone, behavior changed, tests and exact verification results, docs/config changes, secret/cancellation considerations, and intentionally deferred work. Do not claim completion without current command output.
