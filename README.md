# Bqckup Go

A Go-based backup CLI built with Cobra, Viper, GORM, and SQLite.

## Build

```bash
go build -o bqckup ./cmd/bqckup
```

## Commands

```text
bqckup init
bqckup config validate
bqckup backup list
bqckup backup run <site> [--force]
bqckup history list [--site <name>] [--limit <n>]
bqckup version
```

## Development

```bash
make verify
```

Documentation: [configuration](docs/configuration-v2.md), [development](docs/development.md), and [intern backlog](docs/intern-backlog.md).
