# Bqckup Go

Bqckup Go is the CLI-only rewrite of the Python Bqckup application. The current foundation performs real file backups to local storage, records history in SQLite, applies retention, and gives interns stable extension points for exporters and storage adapters.

Web UI, authentication, notifications, restore, Rustic, and Restic are intentionally absent from this milestone.

## Requirements

- Linux
- Go 1.26
- GCC and CGO (required by the official GORM SQLite driver)

## Quick start

```bash
go build -o ./bqckup ./cmd/bqckup
./bqckup --config-dir ./sandbox/config init
```

Edit the generated storage path and file source, then set the example site to `enabled: true`.

```bash
./bqckup --config-dir ./sandbox/config config validate
./bqckup --config-dir ./sandbox/config backup list
./bqckup --config-dir ./sandbox/config backup run example --force
./bqckup --config-dir ./sandbox/config history list --site example
```

The full command contract is:

```text
bqckup init
bqckup config validate
bqckup backup list
bqckup backup run <site> [--force]
bqckup history list [--site <name>] [--limit <n>]
bqckup version
```

Global flags are `--config-dir`, `--output text|json`, and `--verbose`. Production configuration defaults to `/etc/bqckup`; `BQCKUP_CONFIG_DIR` can override it.

## Development

```bash
make verify
sh scripts/check-docs.sh
```

Start with [docs/development.md](docs/development.md). The main references are:

- [architecture](docs/architecture.md)
- [configuration schema v2](docs/configuration-v2.md)
- [testing](docs/testing.md)
- [migration from Python](docs/migration-from-python.md)
- [intern assignments](docs/intern-backlog.md)

## Current status

Implemented: strict schema-v2 config, Cobra CLI, file archive creation, local atomic storage, per-site process locks, SQLite history, interval checks, force runs, cancellation, and retention.

Planned work is split into eleven independently reviewable milestones in the intern backlog. A milestone becomes part of the public config or CLI only when its implementation and tests are complete.
