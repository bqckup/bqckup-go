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

## Storage configuration

Storage definitions live in `<config-dir>/config/storages.yaml` or `storages.yml`. Supported types are `local`, `s3`, and `r2`. IDrive e2 and other S3-compatible providers use `type: s3`; Cloudflare R2 uses `type: r2`.

```yaml
storages:
  local-main:
    type: local
    directory: /var/backups/bqckup
    primary: false

  idrive-main:
    type: s3
    bucket: example-bucket
    access_key_id: EXAMPLE_ACCESS_KEY
    secret_access_key: EXAMPLE_SECRET_KEY
    region: example-region
    endpoint: https://account.region.example.invalid
    primary: true

  cloudflare-archive:
    type: r2
    bucket: example-archive
    access_key_id: EXAMPLE_R2_ACCESS_KEY
    secret_access_key: EXAMPLE_R2_SECRET_KEY
    endpoint: https://example-account.r2.cloudflarestorage.com
    primary: false
```

Copy [the complete example](configs/config/storages.example.yaml) into a runtime configuration directory, replace the non-functional example values, and restrict the credential-bearing file:

```bash
chmod 600 /etc/bqckup/config/storages.yaml
```

Never commit a runtime storage file containing real credentials. A site may name one or more destinations explicitly; when none are listed, the single primary storage is used.

## Database export

Enabled MySQL/MariaDB and PostgreSQL sources are exported with `mysqldump` and `pg_dump` into compressed artifacts such as `databases/application-mysql.sql.gz`. The required binaries must be installed on the backup host.

Runtime site files may contain an inline database password only when the file is a regular, non-symlink file with mode `0600`:

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
```

The password is passed only to the child exporter process through `MYSQL_PWD` or `PGPASSWORD`. Never commit a real password. SQLite export, database repair, and restore remain deferred.

## Development

```bash
make verify
```

Documentation: [configuration](docs/configuration-v2.md), [development](docs/development.md), and [intern backlog](docs/intern-backlog.md).
