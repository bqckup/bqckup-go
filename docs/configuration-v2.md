# Configuration schema v2

Schema v2 keeps the useful split from the Python application while making every file explicit and strictly typed.

```text
<config-dir>/
├── bqckup.yaml
├── config/
│   └── storages.yaml (or storages.yml)
└── sites/
    └── <site>.yaml
```

The default root is `/etc/bqckup`. Precedence is an explicitly supplied Cobra flag, `BQCKUP_` environment override, YAML, then application default. Relative root application paths resolve against `<config-dir>`; file source and local storage paths must be absolute.

## Root file

```yaml
version: 2

app:
  state_database: /var/lib/bqckup/bqckup.db
  temporary_directory: /var/lib/bqckup/tmp
  lock_directory: /var/lib/bqckup/locks
  log_level: info
```

Environment overrides are `BQCKUP_STATE_DATABASE`, `BQCKUP_TEMPORARY_DIRECTORY`, `BQCKUP_LOCK_DIRECTORY`, and `BQCKUP_LOG_LEVEL`.

## Storage file

The storage document is unversioned. Exactly one of `storages.yaml` or `storages.yml` may exist. Supported types are `local`, `s3`, and `r2`.

```yaml
storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true
```

Storage names use lowercase ASCII letters, digits, dots, dashes, and underscores. At most one storage is primary. S3 requires a bucket, inline access keys, and region; R2 additionally requires an HTTPS endpoint and defaults its region to `auto`. Both support an optional safe relative prefix. Custom S3 endpoints require HTTPS except loopback HTTP for disposable tests.

## Database sources

Enabled database sources support `mysql` and `postgres`:

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

Enabled entries require all fields shown. The site file containing a password must be a regular, non-symlink file with exact mode `0600`. Passwords are passed only through `MYSQL_PWD` or `PGPASSWORD` to `mysqldump` or `pg_dump`; they are never rendered in errors, JSON, history, logs, or arguments. Disabled entries may be incomplete planning records. SQLite export, repair, and restore are deferred.

## Site file

The filename must match `site.name`.

```yaml
version: 2

site:
  name: production
  enabled: true
  sources:
    files:
      include:
        - /srv/production/data
      exclude:
        - /srv/production/data/cache
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
```

`minimum_interval` is a positive Go duration such as `30m`, `24h`, or `168h`. `keep_last` must be at least one. Exclusions are absolute paths and apply to the selected source tree. With `follow_symlinks: false`, the archive stores symlinks as symlinks; with `true`, their targets are traversed with cycle detection.

Disabled sites may remain as incomplete placeholders and are not runnable. Enabled sites require at least one absolute file include and either a known explicit destination or one primary storage. Enabled database entries require a supported engine and complete connection fields.

## Strictness and secrets

Unknown keys, an unsupported version, a mismatched filename, relative paths, unknown destination names, or unsupported features fail validation. Errors identify the source file and field path.

Database passwords remain environment references. S3/R2 access keys are inline runtime values, so a credential-bearing storage file must be a regular, non-symlink file with exact mode `0600`. Never commit real credentials. Errors, JSON, history, and logs must not expose keys, endpoints, signed requests, or provider response bodies.

Validate without running a backup:

```bash
bqckup --config-dir /etc/bqckup config validate
bqckup --config-dir /etc/bqckup --output json config validate
```
