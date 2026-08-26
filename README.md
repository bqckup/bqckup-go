# Bqckup Go

CLI backup tool for files and MySQL/PostgreSQL databases. Supports full archive (`.tar.gz`) and incremental deduplicated snapshots (powered by Restic). Destinations can be local filesystem, AWS S3, or Cloudflare R2. Backup history is stored in SQLite.

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
sudo bqckup doctor
sudo bqckup config validate
sudo bqckup backup run [site]
```

Configuration files:

```text
/etc/bqckup/bqckup.yaml
/etc/bqckup/config/storages.yaml
/etc/bqckup/sites/<site>.yaml
```

Use the examples in [`configs/`](configs/) and see [`docs/configuration-v2.md`](docs/configuration-v2.md) for the full schema. S3-compatible settings may be inline or loaded at startup from a constrained HTTPS provider referenced through an environment variable. Keep files with inline credentials at mode `0600` and never commit real credentials.

## Backup modes & storage

- **Full mode (default)**: Compresses files into `.tar.gz` archives and database dumps into `.sql.gz`. Backup sets use readable UTC paths such as `bqckup/example/20-August-2026/00-09-30/`.
- **Incremental mode**: Uses the built-in pure-Go engine (`backup_mode: incremental`) for encrypted, content-defined chunking and deduplication. No external Restic binary is required.
- **Destinations**: Local directory, AWS S3, Cloudflare R2, or any S3-compatible object store.

For a step-by-step walkthrough, see [`docs/guides/incremental-backup-step-by-step.md`](docs/guides/incremental-backup-step-by-step.md).

## Database backups

Enabled MySQL/MariaDB (`engine: mysql`) and PostgreSQL (`engine: postgres`) sources use `mysqldump` and `pg_dump`. Compressed artifacts use names such as `databases/application-mysql.sql.gz`. Passwords are passed through `MYSQL_PWD` or `PGPASSWORD`.

## Commands

```text
bqckup init
bqckup config validate
bqckup doctor [--site <name>]
bqckup backup list
bqckup backup run [site] [--force]
bqckup backup snapshots <site> --destination <name>
bqckup backup restore <site> --destination <name> --snapshot <id|latest> --target <path> [--force]
bqckup history list [--site <name>] [--limit <n>] [--details]
bqckup storage list <destination> --site <site>
bqckup storage link <destination> --key <key> [--expires <n>h]
bqckup version
```

Use `--output json` for machine-readable output and `--config-dir` for a custom configuration directory.
Text history reports logical artifact and destination counts without counting
the same artifact once per destination. Add `--details` to inspect each stored
artifact copy and its object key; JSON remains the raw history format.
`bqckup storage list` shows the live remote contents of one destination: archive
artifacts for full-mode sites, snapshots for incremental sites. It is read-only
and never uses the local history database.
`bqckup backup snapshots` shows the live snapshots of one incremental site,
read directly from its repository on any destination type (local, S3, or R2).
It is read-only and never uses the local history database.
`bqckup backup run` without a site runs every site with `enabled: true` in
configuration order. Supplying a site name continues to run only that site.
`bqckup backup restore` rebuilds the configured paths of one snapshot into
the target directory in restic layout, and never overwrites existing files
without asking (or `--force`). It writes nothing to history.
`bqckup storage link` creates a temporary download link for one full-mode
archive artifact, using the key exactly as `storage list` prints it. Expiry is
in whole hours, 1 to 24, default 24h. The command is read-only on the remote
and never writes history.

## Development

```bash
make verify
sh scripts/check-docs.sh
```

Web UI is not implemented.
