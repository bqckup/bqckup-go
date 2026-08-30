# Bqckup

**Fast, simple, and reliable backups for files and databases.**

Bqckup is a self-hosted command-line backup tool for Linux servers. It backs
up files, MySQL/MariaDB, and PostgreSQL databases to local storage, Amazon S3,
Cloudflare R2, or another S3-compatible service.

> **Backup and forget.** Configure each site once, run Bqckup from your system
> scheduler, and keep control of your data, credentials, and storage provider.

## Features

- File and directory backups
- MySQL/MariaDB and PostgreSQL database exports
- Full `.tar.gz` archives and compressed SQL dumps
- Encrypted, deduplicated incremental file snapshots
- Local, Amazon S3, Cloudflare R2, and S3-compatible storage
- Multiple required destinations per site
- Local SQLite history for every backup run
- Configuration validation and environment diagnostics
- Text and JSON command output
- No external Restic binary required

## Installation

### From a clone

```bash
git clone https://github.com/bqckup/bqckup-go.git
cd bqckup-go
sudo make setup
```

### With the installer

```bash
curl -fsSL https://raw.githubusercontent.com/bqckup/bqckup-go/main/scripts/install.sh | sudo bash
```

### Build manually

```bash
make build
sudo make install
```

Building from source requires Linux, Go 1.26, GCC, and CGO. Install
`mysqldump` or `pg_dump` only for the database types you back up.

## Quick start

Initialize the default configuration tree:

```bash
sudo bqckup init
sudoedit /etc/bqckup/config/storages.yaml
sudoedit /etc/bqckup/sites/example.yaml
sudo chmod 600 /etc/bqckup/config/storages.yaml /etc/bqckup/sites/*.yaml
```

Set the example site's source paths and destination, change `enabled` to
`true`, then validate and run it:

```bash
sudo bqckup config validate
sudo bqckup doctor
sudo bqckup backup run example --force
sudo bqckup history list --details
```

## Configuration

The default configuration directory is `/etc/bqckup`:

```text
/etc/bqckup/bqckup.yaml
/etc/bqckup/config/storages.yaml
/etc/bqckup/sites/<site>.yaml
```

Ready-to-edit examples are in [`configs/`](configs/). Use
`--config-dir <directory>` to load a different configuration tree.

Credential-bearing YAML files must be regular files, not symbolic links, and
must have mode `0600`. Never commit real passwords or storage keys.

## Backup modes

### Full backup

Full mode is the default. It creates portable `.tar.gz` file archives and
compressed `.sql.gz` database dumps below
`bqckup/<server_id>/<site>/<UTC timestamp>/` in each destination.

### Incremental backup

Incremental mode stores encrypted, deduplicated file snapshots below
`bqckup/<server_id>/<site>/incremental-backup/` in each destination. Set `backup_mode: incremental` and set
`incremental.password` directly to the repository password. Keep the site YAML
as a regular, non-symlink file with mode `0600`. The built-in engine is always used.

## Commands

```text
bqckup init
bqckup config validate
bqckup doctor [--site <name>]
bqckup backup list
bqckup backup run <site> [--force]
bqckup backup unlock <site>
bqckup history list [--site <name>] [--limit <n>] [--details]
bqckup version
```

Use `--output json` for machine-readable output. Run `bqckup --help` or any
subcommand with `--help` to see all available options. In text mode,
`backup run` reports each site as soon as it starts and prints its result as
soon as it finishes; JSON mode suppresses progress text.

## Notifications

Add a `notifications:` section to `bqckup.yaml` to get an email, generic
webhook, or Discord webhook when a run finishes. Three events exist:
`backup_failed`, `backup_cancelled`, and `backup_no_change` (successful runs stay
silent). Routes map events to channels; SMTP credentials and webhook URLs are
written directly as `username`, `password`, `url`, and `webhook_url`. A root
config containing these values must be a regular file with mode `0600`. Delivery is best effort: a failing
channel prints a warning and never changes the run result or history. Skipped
runs and preflight failures send nothing. `bqckup config validate` checks the
notification URL format and protected file permissions.

## Scheduling

Run Bqckup with cron, systemd timers, or another scheduler. A daily cron job
for one site could look like this:

```cron
0 2 * * * root /usr/local/bin/bqckup backup run example
```

## Help and contributing

The [User Guide](USER-GUIDE.md) covers complete configuration examples, daily
operations, troubleshooting, and security guidance. Report bugs or propose
features through this repository's GitHub issues and pull requests.

See [CHANGELOG.md](CHANGELOG.md) for release notes. Bqckup is released under
the [MIT License](LICENSE).
