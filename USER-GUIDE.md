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
| notification route `events` | `all`, `backup_failed`, `backup_partial`, `backup_cancelled`, `backup_no_change`, `daily_report`, `monthly_report` |
| `reports.daily.enabled` | `true`, `false` |
| `reports.monthly.enabled` | `true`, `false` |

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
`/usr/bin`, and prepares the default configuration directories. Set `BIN_DIR`
to override the binary installation directory.

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
backup_prefix: hosting_client # optional

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
`0600`. Each line is a JSON object so collectors such as OpenObserve can query
fields including `event`, `site`, `run_id`, `status`, `stage`, `duration_ms`,
and `size_bytes` directly. The default `info` level records the backup plan,
each stage and its duration, stored object keys and sizes, and the final run
summary. `debug` adds sanitized source configuration details. Logs never
include credentials, signed URLs, provider response bodies, or absolute source
paths.

`backup_prefix` is an optional safe relative path used below the `bqckup/`
namespace. With the example above, backup keys start with
`bqckup/hosting_client/207.180.252.231/`. Leave it empty to preserve the
default `bqckup/<server_id>/` layout.

## 4. Configure server and storage

Set the global server identity in `bqckup.yaml`:

```yaml
server_id: 207.180.252.231
backup_prefix: hosting_client # optional
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

The filename is only an organizational label; Bqckup uses `site.name` as the
backup identity. For example, `sites/website-production.yaml` may contain
`name: website`.

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
packages are stored below `bqckup/<server_id>/<site>/<DD-Month-YYYY>/` and use
`<HH-mm-ss>-<package>.gz` names. Packages from one run share the same time
prefix.

Full and incremental results follow the same incomplete-backup model:

- `success` means every source entry inside the configured include/exclude
  scope was read.
- `partial` means one or more child entries disappeared during archive or
  snapshot creation; bqckup retries transient `ENOENT` races, then saves the
  remaining data when an entry is still unavailable. The next run processes a
  previously skipped entry when it becomes available. Other read errors remain
  fatal.
- `failed` means a root source, repository, destination, or database export
  prevented the run from completing normally. Retention cleanup is
  post-backup maintenance: if it fails after the stored backup completes, the
  run remains `success` and reports a warning for the deferred cleanup.
- `cancelled` means the run was stopped before it could complete.

Directory names such as `tmp`, `cache`, and `sessions` are not treated as
ephemeral automatically. Add disposable data to `sources.files.exclude`
explicitly. Excluded paths are outside the backup scope and do not make the
result partial. For a point-in-time view of files that are actively changing,
back up an LVM, ZFS, or Btrfs filesystem snapshot. Continue using database
dumps instead of backing up live database data directories.

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

When Bqckup loads a credential-bearing regular YAML file with a loose mode, it
automatically sets it to `0600`. If the process lacks permission to change the
file, repair it explicitly, then validate again:

```bash
sudo bqckup config fix-permissions
sudo bqckup config validate
```

Automatic and explicit repair never follow symlinks and only set `0600` on
files that contain inline credentials.

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
    # events options: all | backup_failed | backup_partial | backup_cancelled | backup_no_change
    - events: [backup_failed]
      channels: [email]
```

Channel `type` options are `smtp`, `webhook`, and `discord`. Route `events`
options are `all`, `backup_failed`, `backup_partial`, `backup_cancelled`,
`backup_no_change`, `daily_report`, and `monthly_report`. Partial snapshots
send `backup_partial`; successful runs, skipped runs, and preflight failures
send no notification. Delivery is best effort and never changes backup
history or the run result. Keep the root file at mode `0600` when it contains
credentials or URLs.

## Scheduled reports

Bqckup can send a daily backup summary and a monthly consolidated report
through any configured notification channel. Reports are triggered by an
external scheduler calling `bqckup report send daily` or
`bqckup report send monthly`; Bqckup has no internal scheduler.

Each report type is configured in the optional `reports:` section of
`bqckup.yaml`. A named route (`name:` field on the route) connects the report
to its delivery channels.

### Configuration

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
    - name: daily-report-route
      events: [daily_report]
      channels: [email]
    - name: monthly-report-route
      events: [monthly_report]
      channels: [email]

reports:
  daily:
    enabled: true
    timezone: UTC
    schedule:
      time: "08:00"
    notification_route: daily-report-route
    include_empty_days: false
  monthly:
    enabled: true
    timezone: UTC
    schedule:
      day_of_month: 1
      time: "08:00"
    notification_route: monthly-report-route
    include_empty_days: false
```

Report configuration fields:

| Field | Description |
| --- | --- |
| `enabled` | Set to `true` to activate this report type. |
| `timezone` | IANA timezone name, e.g. `UTC`, `America/New_York`, `Asia/Jakarta`. |
| `schedule.time` | Time of day in `HH:MM` format (used by the external scheduler as a reference). |
| `schedule.day_of_month` | Day of month to send the monthly report (1–28, monthly only). |
| `notification_route` | Name of the route in `notifications.routes` that delivers this report. |
| `include_empty_days` | When `true`, include days or sites with no runs in the report. |

The `notification_route` value must match the `name:` field of a route in
`notifications.routes`. Route names are optional for backup-event routes but
required for report routes.

### Sending reports

Send the daily report for today:

```bash
bqckup report send daily
```

Send the daily report for a specific date:

```bash
bqckup report send daily --date 2025-01-15
```

Send the monthly report for the current month:

```bash
bqckup report send monthly
```

Send the monthly report for a specific month:

```bash
bqckup report send monthly --month 2025-01
```

Each report is delivered at most once per period. If the report for a given
date or month has already been sent, the command exits successfully without
sending again.

### Scheduling reports with cron

Add cron entries to trigger reports after your daily backup window:

```cron
# Send daily report at 08:00
0 8 * * * root /usr/bin/bqckup report send daily

# Send monthly report on the 1st of each month at 08:00
0 8 1 * * root /usr/bin/bqckup report send monthly
```

The `schedule.time` and `schedule.day_of_month` fields in `bqckup.yaml` are
reference values for documentation and validation only. The actual delivery
time is controlled entirely by the external scheduler.

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

Text output immediately shows which site and backup mode are running, then
prints each site's result when it finishes. A batch can finish out of
configuration order because one full and one incremental site may run at the
same time. Single-site runs show a loading spinner in an interactive terminal
(or a heartbeat every five seconds when redirected). `--output json` suppresses
these progress lines so stdout remains valid machine-readable JSON.

For a partial full or incremental run, text output also reports how many source
entries could not be read. Retention cleanup warnings are shown after a
successful result and are included in JSON. `history list` records the
sanitized warning for a successful run without changing its status. Bqckup
never stores skipped absolute paths in history or notification payloads.

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

Verify the latest successful backup at one destination:

```bash
bqckup backup check website --destination local-primary
bqckup backup check website --destination local-primary --read-data
```

The first command performs the destination's inexpensive metadata and size
checks. `--read-data` streams the stored content: full-mode packages are
checked against the SHA-256 recorded in history, while incremental repository
data is authenticated by the repository engine. A completed check with
findings exits with status 1; command or storage failures use the normal
categorized exit codes.

If an interrupted incremental operation leaves a stale repository lock:

```bash
bqckup backup unlock website
```

Unlock applies only to incremental sites and removes stale repository locks.
Do not run it while a backup is active.

Before a full archive or database dump with a known size estimate, Bqckup
checks the free space of `app.temporary_directory`. It requires the estimate
plus the larger of 10% or 64 MiB. This protects the local compression/dump
workspace; S3/R2 quota cannot be reliably checked before upload.

To inspect backup processes running on this Linux server:

```bash
bqckup backup active
bqckup --output json backup active --site website
```

`running` means Bqckup verified the lock owner's PID and Linux process start
ticks. `stale` means SQLite still has an unfinished run but no live local lock.
`unknown` is a held lock from an older binary or with invalid metadata; leave
it for manual investigation.

To request a normal shutdown, Bqckup sends SIGTERM and waits for the site lock
to be released. It never sends SIGKILL, removes locks, changes history, or
runs another backup:

```bash
bqckup backup stop website --timeout 1m
```

When one batch process owns more than one site lock, the single-site command
refuses the request and names the affected sites. Review them, then explicitly
stop every verified local backup process if appropriate:

```bash
bqckup backup stop --all --timeout 1m
```

After a graceful stop, the target process records its run as `cancelled`.
If it crashes instead, its unfinished history remains `stale` for review.

## 8. Scheduling

Bqckup does not include a scheduler. Use cron or a systemd timer.

Example cron entry for a daily run at 02:30:

```cron
30 2 * * * /usr/bin/bqckup backup run website
```

To also send a daily report at 08:00 and a monthly report on the 1st:

```cron
30 2 * * * root /usr/bin/bqckup backup run website
30 5 * * 0 root /usr/bin/bqckup backup check website --destination local-primary --read-data
0  8 * * * root /usr/bin/bqckup report send daily
0  8 1 * * root /usr/bin/bqckup report send monthly
```

Use the same operating-system user for scheduled and manual runs. Mixing root
and non-root runs can leave storage or history files with incompatible
ownership. Ensure the scheduler's service account can read the protected site
and storage YAML files. Schedule checks after the backup window; do not run a
data-reading check concurrently with a backup of the same site.

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

Run a restore drill regularly, not only after an incident. Use a new isolated
target, confirm representative files can be opened, import database dumps into
a disposable database, and record the tested backup timestamp. Delete the
drill target only after the result has been reviewed.

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
