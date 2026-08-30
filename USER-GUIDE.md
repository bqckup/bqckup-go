# Bqckup User Guide

This guide covers installation, configuration, daily backup operations,
scheduling, restore, and common failures. It is written for operators of the
`bqckup` command-line application.

## Quick reference

| Setting | Available values |
| --- | --- |
| `app.log_level` | `debug`, `info`, `warn`, `error` |
| `storage.type` | `local`, `s3`, `r2` |
| `backup_mode` | `full` (default), `incremental` |
| database `engine` | `mysql`, `postgres` |
| notification channel `type` | `smtp`, `webhook`, `discord` |
| notification route `events` | `all`, `backup_failed`, `backup_cancelled`, `backup_no_change` |

Values written as `<placeholder>` in this guide must be replaced before use.
They are documentation placeholders, not valid event, channel, or credential
values.

## 1. How Bqckup works

Bqckup reads one configuration tree, backs up one named site at a time, writes
the result to every configured destination, and records the run in SQLite.

File backups have two modes:

- `full` is the default. It creates `files.tar.gz` for files and `.sql.gz`
  files for enabled databases.
- `incremental` stores encrypted and deduplicated file snapshots in a
  Restic-compatible repository. The engine is built into Bqckup; no external
  Restic binary is required for backup.

Database exports remain compressed SQL artifacts in both modes.

## 2. Installation

### Install from a clone

```bash
git clone https://github.com/bqckup/bqckup-go.git
cd bqckup-go
sudo make setup
```

`make setup` builds or downloads the application, installs it in
`/usr/local/bin`, and prepares the default configuration directories.

### Install from a release

```bash
curl -fsSL https://raw.githubusercontent.com/bqckup/bqckup-go/main/scripts/install.sh | sudo bash
```

### Build manually

```bash
make build
sudo make install
```

Building from source requires Go 1.26, GCC, and CGO support.

Install `mysqldump` only when backing up MySQL or MariaDB. Install `pg_dump`
only when backing up PostgreSQL.

## 3. Create the configuration

The default configuration directory is `/etc/bqckup`:

```bash
sudo bqckup init
```

To use another directory:

```bash
bqckup --config-dir /path/to/config init
```

Initialization creates three kinds of files:

```text
bqckup.yaml              application paths
config/storages.yaml     backup destinations
sites/<site>.yaml        sources, mode, destinations, and retention
```

Initialization never overwrites an existing configuration file.

### Application settings

`bqckup.yaml`:

```yaml
server_id: 207.180.252.231

app:
  state_database: /var/lib/bqckup/bqckup.db
  temporary_directory: /var/lib/bqckup/tmp
  lock_directory: /var/lib/bqckup/locks
  log_level: info
  log_file: /var/log/bqckup/bqckup.log # optional; file mode 0600
```

Relative paths are resolved from the configuration directory. Values inside
the YAML are authoritative and are not overridden by environment variables.
`log_level` accepts `debug`, `info`, `warn`, or `error`. When `log_file` is
set, Bqckup appends operational events to that file and creates it with mode
`0600`.

## 4. Configure server and storage

Set the global server identity in `bqckup.yaml`:

```yaml
server_id: 207.180.252.231
```

Each Bqckup installation should use its own stable server ID.

Edit `config/storages.yaml`. Storage, site, and database names may use letters,
numbers, dots, underscores, and hyphens, but must start with a letter or
number.

### Local storage

```yaml
storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true
```

The local directory must be an absolute path.

### Amazon S3 or another S3-compatible service

```yaml
storages:
  object-primary:
    type: s3
    bucket: EXAMPLE_BUCKET
    access_key_id: EXAMPLE_ACCESS_KEY
    secret_access_key: EXAMPLE_SECRET_KEY
    region: us-east-1
    endpoint: https://s3.us-east-1.amazonaws.com
    prefix: production
    primary: true
```

The endpoint may be omitted for standard AWS S3. Custom endpoints must use
HTTPS, except loopback HTTP addresses such as `http://127.0.0.1:9000`.

### Cloudflare R2

```yaml
storages:
  r2-primary:
    type: r2
    bucket: EXAMPLE_BUCKET
    access_key_id: EXAMPLE_R2_ACCESS_KEY
    secret_access_key: EXAMPLE_R2_SECRET_KEY
    region: auto
    endpoint: https://EXAMPLE_ACCOUNT_ID.r2.cloudflarestorage.com
    prefix: production
    primary: true
```

R2 requires `region: auto` and an HTTPS endpoint.

Only one storage may have `primary: true`. An enabled site with no explicit
destinations automatically uses that primary storage.

If `storages.yaml` contains credentials, protect it before validation:

```bash
sudo chmod 600 /etc/bqckup/config/storages.yaml
```

It must be a regular file and must not be a symbolic link.

## 5. Configure a site

The filename must match `site.name`. For example, site `website` belongs in
`sites/website.yaml`.

### Full backup example

```yaml
site:
  name: website
  enabled: true
  backup_mode: full
  sources:
    files:
      include:
        - /srv/website
      exclude:
        - "cache/**"
        - "*.tmp"
      follow_symlinks: false
    databases:
      - name: application-mysql
        enabled: true
        engine: mysql
        host: 127.0.0.1
        port: 3306
        database: application
        username: backup_user
        password: EXAMPLE_DATABASE_PASSWORD
      - name: application-postgres
        enabled: false
        engine: postgres
        host: 127.0.0.1
        port: 5432
        database: application
        username: backup_user
        password: EXAMPLE_DATABASE_PASSWORD
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
```

Important rules:

- Every included file path must be absolute.
- `backup_mode` may be omitted; the default is `full`.
- Disabled database entries may be incomplete.
- Enabled MySQL and PostgreSQL entries require host, port, database, username,
  and password.
- `minimum_interval` prevents runs from starting too frequently. Use
  `--force` to bypass it.
- `keep_last` must be at least `1`.
- Every enabled site needs at least one destination, either explicitly or
  through one primary storage.

A site file containing a database or incremental repository password must have mode `0600`, must be a
regular file, and must not be a symbolic link:

```bash
sudo chmod 600 /etc/bqckup/sites/website.yaml
```

Bqckup passes database passwords to exporters through `MYSQL_PWD` or
`PGPASSWORD`, not through command-line arguments.

### Incremental backup example

Change the site mode and add the repository password directly:

```yaml
site:
  name: website
  enabled: true
  backup_mode: incremental
  incremental:
    password: replace-with-a-strong-repository-password
  sources:
    files:
      include:
        - /srv/website
      exclude:
        - "cache/**"
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
```

The value is stored in this protected YAML file. Keep it at mode `0600`, do
not commit it, and do not add the removed `incremental.engine` field; the
built-in engine is always used.

Incremental repositories are stored below
`bqckup/<server_id>/<site>/incremental-backup/` inside each destination. Full
packages are stored below `bqckup/<server_id>/<site>/<YYYY-MM-DD>/` and use
`<HH-mm-ss>-<package>.gz` names. Packages from one run share the same time
prefix.

## 6. Validate before running

```bash
bqckup config validate
bqckup doctor
```

Use `doctor --site <name>` to limit site-specific checks:

```bash
bqckup doctor --site website
```

`config validate` checks the complete YAML structure. `doctor` also checks
writable application directories, required database tools, and configured
incremental passwords without printing their values.

## Notifications

Add this optional section to the root `bqckup.yaml`:

```yaml
notifications:
  channels:
    email:
      type: smtp
      host: <smtp-host>
      port: 587
      username: <smtp-user>
      password: <smtp-password>
      from: <sender-address>
      to: [<recipient-address>]
  routes:
    # events options: all | backup_failed | backup_cancelled | backup_no_change
    - events: [backup_failed]
      channels: [email]
```

Channel `type` options are `smtp`, `webhook`, and `discord`. Route `events`
options are `all`, `backup_failed`, `backup_cancelled`, and `backup_no_change`.
Successful runs, skipped runs, and preflight failures send no notification.
Delivery is best effort and never changes backup history or the run result.
Keep the root file at mode `0600` when it contains credentials or URLs.

## 7. Daily operations

List configured sites:

```bash
bqckup backup list
```

Run one site:

```bash
bqckup backup run website
```

Ignore `minimum_interval` for one run:

```bash
bqckup backup run website --force
```

Text output immediately shows which site and backup mode are running, shows a
loading spinner in an interactive terminal (or a heartbeat every five seconds
when redirected), then prints that site's result when it finishes. `--output
json` suppresses these progress lines so stdout remains valid machine-readable
JSON.

Storage listing follows the backup mode: full sites show archive objects,
while incremental sites show file snapshots. If an incremental site has an
enabled database source, its database exports appear in a separate
`DATABASE PACKAGES` table. JSON output uses `snapshots` and
`database_packages` fields for that mixed result.

`--force` does not bypass an active site lock. If the command reports
`already_running`, confirm whether another process is backing up the same site
and wait for it to finish or stop a genuinely stuck process before retrying.
Do not delete the lock file while a process may still hold it.

Update the installed Linux binary to the latest release:

```bash
sudo bqckup update
```

The update command shows a spinner in an interactive terminal or a heartbeat
every five seconds when redirected while it downloads, verifies, and installs
the release. Use `--version <version>` to install a specific release.

List recent history:

```bash
bqckup history list
bqckup history list --site website --limit 10
bqckup history list --site website --limit 10 --details
```

Use JSON output for automation:

```bash
bqckup --output json history list --site website
```

If an interrupted incremental operation leaves a stale repository lock:

```bash
bqckup backup unlock website
```

Unlock applies only to incremental sites and removes stale repository locks.
Do not run it while a backup is active.

## 8. Scheduling

Bqckup does not include a scheduler. Use cron or a systemd timer.

Example cron entry for a daily run at 02:30:

```cron
30 2 * * * /usr/local/bin/bqckup backup run website
```

Use the same operating-system user for scheduled and manual runs. Mixing root
and non-root runs can leave storage or history files with incompatible
ownership. Ensure the scheduler's service account can read the protected site
and storage YAML files.

## 9. Restore

The built-in restore command is for incremental sites. Full-mode archives are
restored manually as described below.

List incremental snapshots from a destination:

```bash
bqckup backup snapshots website --destination local-primary
```

Restore the newest snapshot into an explicit directory:

```bash
bqckup backup restore website \
  --destination local-primary \
  --snapshot latest \
  --target /tmp/bqckup-restore
```

Use an ID or ID prefix instead of `latest`. Existing files are never silently
overwritten; review the conflict list and confirm, or use `--force`. Add
`--quiet` to suppress a successful text summary. Restore does not create a
backup-history record.

### Restore a full backup

Copy the required backup set from local, S3, or R2 storage to a temporary
directory. Then extract file and database artifacts manually:

```bash
mkdir -p /tmp/bqckup-restore
tar -xzf files.tar.gz -C /tmp/bqckup-restore
gunzip -c databases/application-mysql.sql.gz > /tmp/application-mysql.sql
```

Import a MySQL dump:

```bash
MYSQL_PWD='database-password' mysql \
  -h 127.0.0.1 -u backup_user application \
  < /tmp/application-mysql.sql
```

Import a PostgreSQL dump:

```bash
PGPASSWORD='database-password' psql \
  -h 127.0.0.1 -U backup_user -d application \
  -f /tmp/application-postgres.sql
```

Restore into a new directory or test database first. Verify the result before
replacing production data.

### Restore an incremental snapshot

The repositories use the standard Restic format, so the official `restic`
command can also be used when needed. Provide the same repository password and
storage credentials, then restore to an explicit empty target directory. Never
restore directly over production data.

## 10. Troubleshooting

| Symptom | Meaning | Action |
| --- | --- | --- |
| `config validation error` | YAML is missing a required field, contains an unknown field, or has an invalid value. | Read the reported file and field, correct it, then run `bqckup config validate`. |
| `must have mode 0600` | A file contains credentials but has unsafe permissions. | Run `chmod 600 <file>` and ensure it is a regular non-symlink file. |
| `required database exporter is unavailable` | `mysqldump` or `pg_dump` is missing. | Install the required database client or disable that database source. |
| `could not export database` | The exporter failed. | Check connectivity, credentials, grants, and whether the database service is running. |
| `could not store backup artifact` | A destination rejected or could not receive data. | Check directory permissions, network access, bucket, endpoint, region, and credentials. |
| `minimum_interval` skip | The previous successful run is too recent. | Wait or run once with `--force`. |
| `already_running` skip | Another process holds this site's backup lock. | Let it finish, or stop a confirmed stuck process, then retry. `--force` does not bypass this lock. |
| Repository lock error | An incremental repository has an active or stale lock. | Confirm no backup is active, then use `bqckup backup unlock <site>` for a stale lock. |

Exit codes:

- `0`: success
- `1`: internal or uncategorized failure
- `2`: invalid command or configuration
- `3`: failed preflight or doctor check
- `4`: backup, storage, cancellation, or execution failure

## 11. Security checklist

- Never commit real passwords, access keys, or secret keys.
- Keep credential-bearing YAML files at mode `0600`.
- Do not use symbolic links for credential-bearing configuration files.
- Use a dedicated operating-system user for scheduled backups.
- Keep incremental repository passwords only in protected site YAML files.
- Treat backup destinations and SQLite history as sensitive data.
- Test restore regularly using an isolated destination.
