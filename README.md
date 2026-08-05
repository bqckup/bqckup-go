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

## Development

```bash
make verify
```

Documentation: [configuration](docs/configuration-v2.md), [development](docs/development.md), and [intern backlog](docs/intern-backlog.md).
