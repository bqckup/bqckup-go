# Configuration

Bqckup reads three YAML files from `/etc/bqckup` by default:

```text
/etc/bqckup/bqckup.yaml
/etc/bqckup/config/storages.yaml
/etc/bqckup/sites/<site>.yaml
```

## Supported values

Use these exact values; configuration decoding is strict.

| Field | Available values |
| --- | --- |
| `app.log_level` | `debug`, `info`, `warn`, `error` |
| `storage.type` | `local`, `s3`, `r2` |
| `site.backup_mode` | `full` (default), `incremental` |
| database `engine` | `mysql`, `postgres` |
| notification channel `type` | `smtp`, `webhook`, `discord` |
| notification route `events` | `all`, `backup_failed`, `backup_cancelled`, `backup_no_change` |

Values shown as `<placeholder>` are documentation placeholders. Replace them
with real values before use.

The root and site files use the current schema automatically, so new YAML files
should omit the `version` field. Existing files that explicitly contain
`version: 2` remain accepted for backward compatibility.

## Root file

```yaml
server_id: 207.180.252.231

app:
  state_database: /var/lib/bqckup/bqckup.db
  temporary_directory: /var/lib/bqckup/tmp
  lock_directory: /var/lib/bqckup/locks
  log_level: info
  log_file: /var/log/bqckup/bqckup.log # optional; file mode 0600
```

Values inside `bqckup.yaml` are authoritative and are not overridden by
environment variables. `BQCKUP_CONFIG_DIR` only selects the configuration
directory when `--config-dir` is omitted. `log_level` accepts `debug`, `info`,
`warn`, or `error`; `log_file` receives operational events and is created with
mode `0600`.

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

  # S3-compatible settings loaded from an HTTPS provider at startup.
  # `url` is the literal provider URL.
  managed-s3:
    type: s3
    credentials:
      source: remote
      url: https://credentials.example.com/bqckup/storage
    prefix: prod-backups                         # optional local override
    primary: false
```

- **Cloudflare R2**: Use `type: r2` with an HTTPS endpoint (`https://<account_id>.r2.cloudflarestorage.com`) and `region: auto`. Credentials must be dedicated **R2 API Tokens** with *Object Read & Write* permissions.
- **AWS S3 / S3-Compatible**: Use `type: s3`. For custom endpoints (e.g. MinIO, Wasabi), set `endpoint` and standard `region`.
- **Remote provider**: `credentials.source: remote` requires `credentials.url`
  to contain an absolute HTTPS URL directly. Remote credentials cannot be mixed with `bucket`,
  `access_key_id`, `secret_access_key`, `region`, or `endpoint` in YAML.
  `prefix` and `primary` remain local settings.
- **Security**: Keep storage files containing inline credentials or provider URLs as regular
  non-symlink files with mode `0600`. A remote provider response is retained
  only in process memory and is never written back to YAML, logs, history, or
  command output.

The remote provider is called with `GET` when an application command opens the
configuration. `bqckup doctor` resolves remote storages one at a time before
probing them; a provider or validation failure becomes a failing
`storage:<name>` check instead of a hard error. A successful response must be a
small, strict JSON object:

```json
{
  "bucket": "example-bucket",
  "access_key_id": "REDACTED",
  "secret_access_key": "REDACTED",
  "endpoint": "https://s3-compatible.example",
  "region": "example-region"
}
```

Unknown fields, malformed or oversized JSON, non-success HTTP responses,
timeouts, and invalid returned storage settings fail preflight with a redacted
message. Provider URLs and response bodies are never included in errors.
`bqckup config validate` validates the local declaration without contacting the
provider; commands that open the application fetch and validate the response.
Older Bqckup versions reject the new `credentials` block because configuration
decoding is strict, so upgrade the binary before deploying this form.


## Site file

The filename must match `site.name`:

```yaml
site:
  name: example
  enabled: true
  # backup_mode accepts 'full' (default) or 'incremental'
  backup_mode: incremental
  incremental:
    # Literal repository password; protect this file with mode 0600.
    # Incremental backup always uses Bqckup's built-in pure-Go engine.
    password: replace-with-a-strong-repository-password
  sources:
    files:
      include:
        - /srv/example/data
      exclude:
        - "*.tmp"
        - "cache/**"
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

- **Backup Mode**: `backup_mode` defaults to `full` (`.tar.gz` archive). When set to `incremental`, Bqckup's built-in pure-Go engine creates Restic-format-v2 deduplicated file snapshots; no external Restic executable is required.
- **Incremental Password**: `incremental.password` contains the repository encryption password directly. The site file must be a regular, non-symlink file with exact mode `0600`; the value is never logged or printed.
- **File Excludes**: `sources.files.exclude` accepts absolute paths or glob patterns relative to each include root. Basename globs such as `*.tmp` match at any depth; use a trailing `/**`, for example `cache/**`, to exclude a directory recursively. These semantics are shared by full and incremental backups.
- **Removed field**: `incremental.engine` is no longer accepted. Remove it from existing site files. Restic format-v1 repositories must be migrated to format v2 separately before use.
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
`bqckup config validate` additionally checks notification URLs and requires a
credential-bearing `bqckup.yaml` to be a regular, non-symlink file with mode
`0600`.

## Notifications

Optional top-level `notifications:` section in the root file. Absent section
means notifications are off.

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
      to:
        - <recipient-address>

    webhook:
      type: webhook
      url: <webhook-url>

    discord:
      type: discord
      webhook_url: <discord-webhook-url>

  routes:
    # events options: all | backup_failed | backup_cancelled | backup_no_change
    - events: [backup_failed]
      channels: [email, discord]
```

- **Channels** are named, one of three types. `smtp` requires `host`,
  `port`, `from`, and a non-empty `to`; `webhook` requires `url`;
  `discord` requires `webhook_url`. Fields foreign to the channel type
  are rejected, as are `username`/`password` provided one without
  the other.
- **Credentials and URLs are literal config values** (`username`, `password`,
  `url`, `webhook_url`). Because these values may be sensitive, a root config
  containing any of them must be a regular, non-symlink file with exact mode
  `0600`. Webhook URLs must be absolute HTTP(S) URLs; non-loopback endpoints
  require HTTPS.
- **Routes** map events to channels. Events are `backup_failed`,
  `backup_cancelled`, and `backup_no_change` (successful runs stay silent); a
  route needs at least one event and may fan out to several channels. A channel
  matched through several routes is sent once per event. Duplicate channel
  names in the YAML map are not detected: the last definition wins (yaml map
  semantics).
- **Per-event notifications are opt-in**. For a quiet installation, leave the
  `notifications` block unset and rely on the scheduled daily/monthly report
  routes instead. This keeps operational monitoring in aggregate reports rather
  than sending a message for every backup event.
- **Delivery**: after a run is recorded terminal in history, every channel of
  every matching route is attempted, sequentially, with the same sanitized
  payload (machine hostname and IP, run id, site, status, timestamps,
  duration, package count, size, destinations, and for failed, cancelled, or
  unchanged runs a redacted error category and message).
  A failing channel never stops the others and never changes the run status
  or history. Skipped runs and preflight failures send nothing. Webhook and
  Discord use a 10-second HTTP timeout; SMTP uses a 30-second session
  deadline. SMTP port 465 is implicit TLS; other ports use STARTTLS, and
  PLAIN authentication is only attempted when the session is encrypted.

Running bqckup in a container: the payload's `hostname` and `server_ip`
describe the container (its ID and bridge IP), not the host. Set `hostname:`
on the compose service to make it readable. The human date in email and
Discord renders in the process-local timezone; set `TZ` on the service or
the date stays UTC.
