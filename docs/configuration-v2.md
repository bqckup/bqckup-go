# Configuration schema v2

Schema v2 keeps the useful split from the Python application while making every file explicit and strictly typed.

```text
<config-dir>/
├── bqckup.yaml
├── config/
│   └── storages.yaml
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

The foundation accepts `type: local` only.

```yaml
version: 2

storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
```

Storage names use lowercase ASCII letters, digits, dots, dashes, and underscores. S3 fields exist in typed design contracts for later milestones but validation intentionally rejects S3 until its adapter and credential behavior are implemented.

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

Disabled sites may remain as incomplete placeholders and are not runnable. Enabled sites require at least one absolute file include and one known destination. Database entries are rejected when enabled until their exporter milestone is delivered.

## Strictness and secrets

Unknown keys, an unsupported version, a mismatched filename, relative paths, unknown destination names, or unsupported features fail validation. Errors identify the source file and field path.

YAML contains environment-variable names, never secret values. Future database sources use `password_env`; future S3 providers use `access_key_env`, `secret_key_env`, or `url_env`. Static validation must not resolve or contact those providers.

Validate without running a backup:

```bash
bqckup --config-dir /etc/bqckup config validate
bqckup --config-dir /etc/bqckup --output json config validate
```
