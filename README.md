# Bqckup Go

CLI backup tool for files and MySQL/PostgreSQL databases. Destinations can be local, S3, or Cloudflare R2. Backup history is stored in SQLite.

## Quick start

```bash
# Automated setup (from repository clone)
sudo make setup
# or: sudo ./scripts/install.sh

# Standalone setup (downloads pre-built release binary + verifies SHA-256)
curl -fsSL https://raw.githubusercontent.com/bqckup/bqckup-go/main/scripts/install.sh | sudo bash

# Or manual setup
make build && sudo make install
sudo bqckup init
sudo bqckup config validate
sudo bqckup backup run <site>
```

Configuration files:

```text
/etc/bqckup/bqckup.yaml
/etc/bqckup/config/storages.yaml
/etc/bqckup/sites/<site>.yaml
```

Use the examples in [`configs/`](configs/) and see [`docs/configuration-v2.md`](docs/configuration-v2.md) for the full schema. Keep credential-bearing files at mode `0600` and never commit real credentials.

## Database backups

Enabled MySQL/MariaDB (`engine: mysql`) and PostgreSQL (`engine: postgres`) sources use `mysqldump` and `pg_dump`. Compressed artifacts use names such as `databases/application-mysql.sql.gz`. Passwords are passed through `MYSQL_PWD` or `PGPASSWORD`.

## Commands

```text
bqckup init
bqckup config validate
bqckup backup list
bqckup backup run <site> [--force]
bqckup history list [--site <name>] [--limit <n>] [--details]
bqckup version
```

Use `--output json` for machine-readable output and `--config-dir` for a custom configuration directory.
Text history reports logical artifact and destination counts without counting
the same artifact once per destination. Add `--details` to inspect each stored
artifact copy and its object key; JSON remains the raw history format.

## Development

```bash
make verify
sh scripts/check-docs.sh
```

Restore, SQLite source export, Restic, and web UI are not implemented yet.
