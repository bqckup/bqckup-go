# MySQL and PostgreSQL Database Exporters

## Goal

Add the first database-exporter milestone to the CLI backup runner. Enabled MySQL/MariaDB and PostgreSQL sources produce compressed, checksummed SQL artifacts that follow the existing all-destinations storage and retention rules.

This milestone does not implement SQLite export, database repair, restore, notification email, or legacy configuration migration.

## Configuration contract

Database sources remain under each site’s `sources.databases` list:

```yaml
sources:
  databases:
    - name: application-mysql
      enabled: true
      engine: mysql
      host: localhost
      port: 3306
      database: application
      username: backup_user
      password: <runtime-secret>

    - name: application-postgres
      enabled: true
      engine: postgres
      host: localhost
      port: 5432
      database: application
      username: backup_user
      password: <runtime-secret>
```

For enabled sources, `name`, `engine`, `host`, `port`, `database`, `username`, and `password` are required. `engine` accepts exactly `mysql` or `postgres`. Ports must be between 1 and 65535. Inline passwords are permitted only in a runtime site file protected as a regular, non-symlink file with exact mode `0600`. Password values are never stored in history, arguments, logs, errors, or test fixtures.

Disabled database entries may be retained as incomplete planning records and do not require runtime connectivity. The existing site and destination validation remains unchanged.

## Architecture

The exporter boundary belongs to `internal/backup/database`, while concrete process execution remains behind an adapter-owned process interface:

```go
type Exporter interface {
    Export(ctx context.Context, source config.DatabaseSource, destination string) (backup.Artifact, error)
}
```

The MySQL and PostgreSQL adapters are concrete implementations selected by `internal/app`. The runner depends only on the exporter interface and existing artifact/storage contracts. It does not import Cobra, Viper, GORM, AWS SDK types, or `os/exec`.

Each adapter invokes its native tool with `exec.CommandContext` and explicit argument slices:

- MySQL uses `mysqldump` with host, port, username, `--single-transaction`, `--quick`, routines, and triggers.
- PostgreSQL uses `pg_dump` with host, port, username, plain format, `--no-owner`, and `--no-privileges`.

Passwords are copied only into the child process environment as `MYSQL_PWD` or `PGPASSWORD`. Password values and complete child environments are never rendered or persisted.

## Artifact flow

For each enabled database source, the adapter streams SQL output through gzip into an owner-only temporary file. On successful completion it computes the file size and SHA-256 and returns:

```text
SourceKind: database
SourceName: <configured source name>
Object key: bqckup/<site>/<UTC timestamp>/databases/<source-name>.sql.gz
```

The source name is validated by the existing safe-name rule, so it cannot escape the artifact key layout. A failed process, cancellation, close error, or checksum error removes the partial file.

The runner processes the file archive and each enabled database source sequentially. Every completed artifact is uploaded to every configured destination and recorded in history. Any export, upload, or history failure fails the run and prevents retention. A failed current run never deletes a previously successful backup set.

## Error handling and cancellation

Every subprocess receives the runner context. Cancellation terminates the process, removes the partial compressed artifact, records the run as cancelled, and does not start retention.

Missing binaries are preflight failures. Non-zero exits, invalid source output, file I/O failures, and destination failures are backup/storage failures. Public errors use stable categorized messages such as `could not export database`; command arguments, passwords, hostnames where unnecessary, stderr bodies, and provider details are not exposed.

The adapter may retain an internal wrapped cause for `errors.Is`/`errors.As`, but its public `Error()` text is redacted. No application-level retry is added in this milestone.

## Testing strategy

- Configuration tests cover valid MySQL/PostgreSQL sources, missing fields, invalid ports, unsupported engines, invalid environment names, and disabled incomplete entries.
- Process contract tests use a fake process runner to verify executable, argument ordering, password environment key, cancellation, non-zero exit handling, and absence of password values from errors.
- Artifact tests verify gzip output, deterministic source metadata, SHA-256/size, owner-only permissions, and cleanup after cancellation or failure.
- Runner tests cover multiple database sources, all-required destinations, history recording, and retention suppression after exporter failure.
- Default tests never require MySQL/PostgreSQL binaries, network access, or credentials. Disposable database integration tests remain opt-in.

## Deferred work

SQLite export, PostgreSQL-specific advanced options, database repair/retry, restore, environment credential providers, remote credential providers, and Restic remain separate milestones.
