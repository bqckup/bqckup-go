# Development guide

## First setup

Install Go 1.26, GCC, and CGO support. Clone the repository, then run:

```bash
go mod download
make verify
sh scripts/check-docs.sh
```

Use a feature branch and keep each commit focused. Do not develop against production configuration or credentials. `bqckup init` can create a disabled example in a temporary directory.

## Change workflow

1. Read the architecture, config contract, and selected backlog milestone.
2. Write an observable failing test for the requested behavior.
3. Run the smallest test command and confirm the intended failure.
4. Implement the smallest coherent behavior that satisfies the test.
5. Refactor only while tests remain green.
6. Run package tests, then `make verify` and the documentation check.
7. Update examples and docs when a CLI, config, persistence, or adapter contract changes.

Do not expose config for unfinished functionality and do not commit production stubs returning “not implemented.” An intern PR should implement one backlog milestone unless a prerequisite change is explicitly approved.

## Coding conventions

- Accept `context.Context` at I/O and use-case boundaries.
- Wrap causes so `errors.Is` and `errors.As` work.
- Present only categorized and redacted errors at the CLI boundary.
- Keep Cobra logic in `internal/cli`, Viper in `internal/config`, GORM in `internal/history`, and concrete wiring in `internal/app`.
- Prefer a small consumer-owned interface over a broad shared abstraction.
- Use UTC for persisted and object-key timestamps.
- Never assemble shell command strings. Database exporters must use `exec.CommandContext` with explicit arguments and controlled environment variables.
- Use `gofmt`; do not hand-format Go.

## Commands

```bash
make fmt
make vet
make test
make build
make verify
sh scripts/check-docs.sh
```

For a quick isolated run:

```bash
go run ./cmd/bqckup --config-dir ./sandbox/config init
go run ./cmd/bqckup --config-dir ./sandbox/config config validate
```

The generated example is disabled. Change all paths to disposable local directories before enabling it.

## Pull-request handoff

Describe the selected milestone, behavior added, tests run, config/docs changes, security considerations, and anything intentionally left out. Reviewers should be able to verify the PR without production infrastructure.
