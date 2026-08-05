# Bqckup Go

CLI-only backup tool for file sources. Backups can be stored locally or in S3-compatible storage, including Cloudflare R2. Run history is kept in SQLite.

## Quick start

Build the CLI and create a configuration tree:

```bash
go build -o bqckup ./cmd/bqckup
./bqckup --config-dir /etc/bqckup init
./bqckup --config-dir /etc/bqckup config validate
```

Edit the generated files, enable a site, then run a backup:

```bash
./bqckup --config-dir /etc/bqckup backup list
./bqckup --config-dir /etc/bqckup backup run <site>
```

The default configuration layout is:

```text
/etc/bqckup/
├── bqckup.yaml
├── config/storages.yaml
└── sites/<site>.yaml
```

## Minimal configuration

Local storage:

```yaml
storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true
```

S3-compatible storage (also works for R2 with `type: r2`):

```yaml
storages:
  remote-primary:
    type: s3
    bucket: example-bucket
    access_key_id: EXAMPLE_ACCESS_KEY
    secret_access_key: EXAMPLE_SECRET_KEY
    region: example-region
    endpoint: https://s3.example.invalid
    primary: true
```

Site configuration:

```yaml
version: 2

site:
  name: example
  enabled: true
  sources:
    files:
      include:
        - /srv/example/data
      follow_symlinks: false
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
```

Keep credential-bearing storage files at mode `0600`, and never commit real credentials.

Example files are available in [`configs/`](configs/) and the full schema is documented in [`docs/configuration-v2.md`](docs/configuration-v2.md).

## Commands

```text
bqckup init
bqckup config validate
bqckup backup list
bqckup backup run <site> [--force]
bqckup history list [--site <name>] [--limit <n>]
bqckup version
```

Use `--output json` for machine-readable output. Use `--config-dir` to select another configuration directory.

## Development

```bash
make verify
sh scripts/check-docs.sh
```

Database export, restore, Restic, and web UI are intentionally out of scope for the current CLI foundation.
