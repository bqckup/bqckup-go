# Configuration

Bqckup reads three YAML files from `/etc/bqckup` by default:

```text
/etc/bqckup/bqckup.yaml
/etc/bqckup/config/storages.yaml
/etc/bqckup/sites/<site>.yaml
```

The root and site files use schema v2 automatically. The `version` field is optional; if it is present, it must be `2`.

## Root file

```yaml
app:
  state_database: /var/lib/bqckup/bqckup.db
  temporary_directory: /var/lib/bqckup/tmp
  lock_directory: /var/lib/bqckup/locks
  log_level: info
```

Environment overrides: `BQCKUP_STATE_DATABASE`, `BQCKUP_TEMPORARY_DIRECTORY`, `BQCKUP_LOCK_DIRECTORY`, and `BQCKUP_LOG_LEVEL`.

## Storage file

Supported types are `local`, `s3`, and `r2`:

```yaml
storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true

  remote-primary:
    type: s3
    bucket: example-bucket
    access_key_id: EXAMPLE_ACCESS_KEY
    secret_access_key: EXAMPLE_SECRET_KEY
    region: example-region
    endpoint: https://s3.example.invalid
    primary: false
```

Use `type: r2` for Cloudflare R2. R2 requires an HTTPS endpoint and uses region `auto`. Keep credential-bearing storage files as regular files with mode `0600`.

## Site file

The filename must match `site.name`:

```yaml
site:
  name: example
  enabled: true
  sources:
    files:
      include:
        - /srv/example/data
      follow_symlinks: false
    databases:
      - name: application-mysql
        enabled: true
        engine: mysql
        host: localhost
        port: 3306
        database: application
        username: backup_user
        password: <runtime-secret>
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
```

Database engines are `mysql` and `postgres`. MySQL/MariaDB uses `mysqldump`; PostgreSQL uses `pg_dump`. Passwords are passed through `MYSQL_PWD` or `PGPASSWORD`. A password-bearing site file must be a regular file with mode `0600`.

## Validate

```bash
bqckup --config-dir /etc/bqckup config validate
bqckup --config-dir /etc/bqckup --output json config validate
```

Unknown keys, invalid paths, unsupported types, and explicit versions other than `2` are rejected.
