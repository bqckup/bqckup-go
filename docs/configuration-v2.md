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
  # Local disk destination
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true

  # AWS S3 destination
  offsite-s3:
    type: s3
    bucket: example-backup-bucket
    access_key_id: EXAMPLE_ACCESS_KEY
    secret_access_key: EXAMPLE_SECRET_KEY
    region: us-east-1
    endpoint: https://s3.us-east-1.amazonaws.com # optional for standard AWS
    prefix: prod-backups                         # optional
    primary: false

  # Cloudflare R2 destination (S3-compatible)
  offsite-r2:
    type: r2
    bucket: example-r2-bucket
    access_key_id: EXAMPLE_R2_ACCESS_KEY
    secret_access_key: EXAMPLE_R2_SECRET_KEY
    endpoint: https://<account_id>.r2.cloudflarestorage.com
    region: auto                                 # Cloudflare R2 uses 'auto'
    prefix: prod-backups                         # optional
    primary: false
```

- **Cloudflare R2**: Use `type: r2` with an HTTPS endpoint (`https://<account_id>.r2.cloudflarestorage.com`) and `region: auto`. Credentials must be dedicated **R2 API Tokens** with *Object Read & Write* permissions.
- **AWS S3 / S3-Compatible**: Use `type: s3`. For custom endpoints (e.g. MinIO, Wasabi), set `endpoint` and standard `region`.
- **Security**: Keep credential-bearing storage files as regular files with mode `0600`.


## Site file

The filename must match `site.name`:

```yaml
site:
  name: example
  enabled: true
  # backup_mode accepts 'full' (default) or 'incremental'
  backup_mode: incremental
  incremental:
    engine: restic
    password_env: RESTIC_PASSWORD
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

- **Backup Mode**: `backup_mode` defaults to `full` (`.tar.gz` archive). When set to `incremental`, Bqckup uses `restic` for deduplicated file snapshots.
- **Incremental Password**: `incremental.password_env` references the runtime environment variable holding the repository encryption password (plaintext passwords in YAML are strictly rejected).
- **Database engines**: `mysql` and `postgres`. MySQL/MariaDB uses `mysqldump`; PostgreSQL uses `pg_dump`. Passwords are passed through `MYSQL_PWD` or `PGPASSWORD`. A password-bearing site file must be a regular file with mode `0600`.

## Diagnostics (Doctor)

Run preflight checks to verify configuration, permissions, and tool dependencies:

```bash
bqckup --config-dir /etc/bqckup doctor
bqckup --config-dir /etc/bqckup --output json doctor
```

## Validate

```bash
bqckup --config-dir /etc/bqckup config validate
bqckup --config-dir /etc/bqckup --output json config validate
```

Unknown keys, invalid paths, unsupported types, and explicit versions other than `2` are rejected.
