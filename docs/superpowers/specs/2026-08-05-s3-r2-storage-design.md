# S3 and R2 Storage Design

**Status:** Approved in design review on 2026-08-05

## Goal

Add production-capable `local`, `s3`, and `r2` storage types to the CLI-only Bqckup Go application. The remote adapters must upload verified artifacts, avoid overwrites, preserve the existing all-required destination semantics, and support the retention operations already required by the storage contract.

## Scope decision

This feature combines the S3 adapter work with the minimum prefix-scoped listing and deletion behavior needed by the existing runner. The current `storage.Store` contract requires `Put`, `ListBackupSets`, and `Delete`, and the runner applies retention after all required uploads succeed. A write-only remote adapter would therefore make every remote backup fail after upload.

The user also approved a deliberate exception to the earlier environment-reference design: runtime S3 credentials are stored directly in `config/storages.yml` or `config/storages.yaml`. This exception is constrained by strict file-permission checks, redaction requirements, and a rule that real runtime configuration never enters Git.

## Non-goals

- Database corruption retries or automatic database repair.
- Restore commands or object downloads.
- Bucket creation or bucket lifecycle management.
- Remote HTTP credential providers, environment credential references, or AWS shared-profile selection.
- Restic or Rustic integration.
- Provider-specific storage classes, ACLs, object lock, or server-side encryption configuration.
- Unbounded retries or provider-specific retry tuning.
- Web UI, scheduler, notifications, or reporting.

## Configuration contract

### File discovery

The application accepts either:

```text
<config-dir>/config/storages.yaml
<config-dir>/config/storages.yml
```

If both files exist, configuration loading fails with an ambiguity error. The storage document no longer contains a `version` field. The root `bqckup.yaml` and site documents remain schema version 2.

The document keeps the legacy top-level `storages` mapping:

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
    prefix: company-backups
    primary: true

  cloudflare-archive:
    type: r2
    bucket: example-archive
    access_key_id: EXAMPLE_R2_ACCESS_KEY
    secret_access_key: EXAMPLE_R2_SECRET_KEY
    endpoint: https://example-account.r2.cloudflarestorage.com
    primary: false
```

Repository examples use non-functional example values only. Real keys are permitted only in a runtime configuration tree outside the repository.

### Common fields

- `type` is required and accepts `local`, `s3`, or `r2`.
- `primary` is optional and defaults to `false`.
- YAML boolean forms `true` and `false` are canonical. Legacy `yes` and `no` values remain accepted when decoding into the typed boolean field.
- At most one storage may be primary.
- Storage names retain the existing lowercase ASCII letters, digits, dots, dashes, and underscores rule.
- Unknown fields fail exact decoding.

### Local fields

- `directory` is required and must be absolute.
- Bucket, credential, region, endpoint, and prefix fields are rejected.

### S3 fields

- `bucket`, `access_key_id`, `secret_access_key`, and `region` are required.
- `endpoint` is optional. When omitted, the AWS SDK resolves the standard AWS S3 endpoint for the configured region.
- A custom endpoint supports IDrive e2, MinIO, and other S3-compatible services.
- `prefix` is optional. When set, it is validated as a relative safe storage key and is prepended to every Bqckup object key.
- S3-only fields such as ACL, storage class, and force-path-style are not exposed in the first release.

### R2 fields

- `bucket`, `access_key_id`, `secret_access_key`, and `endpoint` are required.
- `region` is optional and defaults to `auto`.
- The endpoint must use the Cloudflare R2 HTTPS endpoint form supplied for the account.
- `prefix` has the same behavior as S3.

### Endpoint safety

- Production endpoints must use HTTPS.
- HTTP is accepted only when the hostname is `localhost`, `127.0.0.1`, or `::1`, enabling disposable MinIO integration tests.
- Endpoints containing user information, query strings, or fragments are rejected.
- Endpoint values are never included in user-facing errors, JSON, SQLite history, or logs.

### Primary selection

Enabled sites may continue to declare explicit destinations. When an enabled site has no destinations, the single primary storage becomes its destination. Validation fails when a site has no explicit destinations and no primary storage exists.

Explicit site destinations always win. The primary flag does not add an extra destination to a site that already lists one or more destinations.

### Credential-file permissions

If any storage contains `access_key_id` or `secret_access_key`, the selected storage file must have mode `0600`. Symlinks are rejected for credential-bearing storage files. Permission validation occurs before the application constructs an SDK client or contacts a provider.

The credential values remain in memory only for the process lifetime. They are not copied into command arguments, environment variables, SQLite, generated examples, error strings, or test fixtures.

## Architecture

### Package boundaries

- `internal/config` discovers the storage file, strictly decodes it, applies R2 defaults, validates field combinations, resolves primary destinations, and enforces credential-file permissions.
- `internal/storage` continues to own portable artifact and backup-set types. Shared object-key validation is moved here so local and remote adapters apply the same rules.
- `internal/storage/local` remains the local filesystem adapter.
- `internal/storage/s3compat` implements both `s3` and `r2` through AWS SDK for Go v2.
- `internal/app` is the only package that converts validated configuration into concrete local or S3-compatible adapters.
- `internal/backup` continues to depend only on the consumer-owned `storage.Store` interface and never imports AWS SDK types.

### S3-compatible client construction

The adapter receives a provider kind, bucket, region, optional endpoint, optional prefix, and static credentials. It constructs an AWS SDK v2 S3 client with a standard retryer limited to three attempts and an AWS SDK v2 S3 Transfer Manager client for object uploads.

When `endpoint` is empty, the SDK's standard S3 endpoint resolver is used. A configured endpoint is supplied through the SDK v2 `BaseEndpoint` mechanism. Custom S3 and R2 endpoints use path-style bucket addressing for compatibility with account-specific and local endpoints.

R2 is not a separate implementation. Its constructor applies the `auto` region default and R2 validation, then delegates to the same adapter.

The raw SDK and Transfer Manager clients are hidden behind the smallest interfaces required by the adapter. Unit tests use fakes at these external boundaries; the runner and domain packages never mock or import the SDK.

## Object keys

The existing key layout remains canonical:

```text
bqckup/<site>/<UTC timestamp>/<artifact name>
```

When a storage prefix is configured, the final key is:

```text
<prefix>/bqckup/<site>/<UTC timestamp>/<artifact name>
```

Keys reject absolute paths, backslashes, empty segments, dot segments, and escaping paths. Names continue to originate from validated configuration rather than raw runtime input.

## Upload lifecycle

1. Check context cancellation before any file or network operation.
2. Validate the artifact path, expected size, and SHA-256 value.
3. Open the artifact and verify its local size before upload.
4. Upload through the AWS SDK v2 S3 Transfer Manager `UploadObject` operation with `If-None-Match: *`, the expected multipart object size, and metadata keys `bqckup-sha256` and `bqckup-size`.
5. Treat an existing key as a storage collision; never overwrite it, even when the contents are identical.
6. Use `HeadObject` to verify the stored content length and Bqckup checksum metadata.
7. If remote verification fails after a successful upload, attempt a best-effort deletion of the exact object created by this operation, then return the original verification failure joined with any cleanup failure.
8. Return the stable key, verified size, and SHA-256 to the runner for SQLite history recording.

The Transfer Manager performs automatic multipart upload for large files and aborts an incomplete multipart upload when the operation fails or is cancelled. The first release keeps its default part sizing and concurrency. Acceleration and provider-specific multipart controls remain deferred.

## Retention behavior

`ListBackupSets` uses paginated `ListObjectsV2` calls restricted to the configured storage prefix plus `bqckup/<site>/`. It extracts only keys whose next segment is a valid UTC Bqckup timestamp. Unrelated objects, malformed timestamps, and keys outside the site prefix are ignored.

`Delete` accepts only a validated Bqckup backup-set prefix. It paginates all objects below that prefix and deletes them in bounded S3 batches. An empty or bucket-root prefix is rejected. The first provider failure stops deletion and is returned as a categorized storage error.

Retention runs only after all required destination uploads and history writes succeed, preserving the current runner invariant. A failed upload, cancellation, or failed current run never deletes a previous successful backup set.

## Retry and cancellation

- SDK retries are limited to three attempts and only apply to errors classified as retryable by the SDK.
- Application code does not retry invalid credentials, access denial, validation failures, checksum mismatches, or existing-object collisions.
- Every SDK call receives the command context.
- Cancellation stops upload, head, list, or delete operations and maps to the existing cancellation category.
- Retry errors are wrapped without endpoint URLs, request payloads, response bodies, access keys, or secret keys.

## Error and redaction contract

Adapter errors preserve causes for `errors.Is` and `errors.As`, while public output uses only stable categorized messages such as:

```text
could not prepare a storage destination
could not store backup artifact
backup completed but retention could not be applied
backup was cancelled
```

The following values must never appear in text output, JSON output, SQLite history, logs, or returned user messages:

- access key IDs;
- secret access keys;
- custom endpoints;
- SDK request URLs or signed headers;
- provider response bodies;
- child or process environments.

## Testing strategy

### Configuration tests

- Load canonical local, S3, and R2 documents.
- Accept `.yaml` and `.yml`; reject both existing simultaneously.
- Reject a storage-level `version` field.
- Apply the R2 `auto` region default.
- Accept canonical booleans and legacy `yes`/`no` values.
- Reject multiple primary storages and resolve a single primary for a site without destinations.
- Reject missing required fields, cross-type fields, unsafe prefixes, unsafe endpoints, symlinked credential files, and non-`0600` credential files.
- Prove that validation errors do not contain credential values or endpoint values.

### Adapter tests

- Shared key-safety and stored-artifact contract with the local adapter.
- Successful upload input, conditional write, metadata, head verification, and stable return values.
- Existing-object collision without overwrite.
- Upload failure, verification failure, and exact-object cleanup behavior.
- Context cancellation for upload, head, list, and delete.
- Bounded retry configuration and redacted provider failures.
- Paginated backup-set discovery, invalid timestamp filtering, foreign prefix isolation, empty results, bounded deletion, and partial deletion failure.

### Runner and application tests

- Concrete construction for `local`, `s3`, and `r2` without leaking SDK types.
- Mixed local and remote destinations preserve all-required behavior.
- A remote failure records a failed artifact and prevents retention.
- Primary fallback applies only when site destinations are absent.
- Text, JSON, and SQLite history remain free of credentials and endpoints.

### Live integration test

The default test suite never needs a network or real credentials. An explicit opt-in integration test may use a disposable bucket and a private runtime config. It creates one uniquely named small object below a dedicated integration prefix, verifies it, lists it, and deletes it. Cleanup runs even when verification fails.

The supplied credential pair is considered exposed because it was pasted into a conversation. It may be used only for disposable verification and must be rotated before production use.

## Documentation and migration

The implementation pull request updates:

- `docs/configuration-v2.md` with the unversioned storage document and all type contracts;
- `docs/architecture.md` with the S3-compatible adapter and remote retention flow;
- `docs/testing.md` with the opt-in integration test;
- `docs/intern-backlog.md` to mark the combined approved slice and preserve deferred work;
- `configs/config/storages.yaml` with a secret-free local example;
- the `bqckup init` template with no real or functional credentials;
- the repository development skill references so future agents follow the approved contract.

Existing versioned `config/storages.yaml` files must remove their top-level `version` field. Existing local storage entries add `type: local` if it is not already present. Legacy S3-like entries add `type: s3`; R2 entries add `type: r2`.

No automated migration command is included. Strict validation identifies the exact field that must be changed.

## Technical references

- [AWS SDK for Go v2 endpoint configuration](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-endpoints.html)
- [AWS S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)
- [AWS SDK for Go v2 S3 Transfer Manager](https://pkg.go.dev/github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager)
- [Cloudflare R2 S3 API compatibility](https://developers.cloudflare.com/r2/api/s3/api/)
- [Cloudflare R2 AWS SDK for Go example](https://developers.cloudflare.com/r2/examples/aws/aws-sdk-go/)
- [IDrive e2 S3-compatible API](https://www.idrive.com/s3-storage-e2/s3-compatible-api)
- [IDrive e2 endpoint and region behavior](https://www.idrive.com/s3-storage-e2/region-endpoints)

## Acceptance criteria

- A site can back up to local, AWS S3, IDrive e2 or another S3-compatible endpoint, and Cloudflare R2 through explicit storage types.
- A site can use an explicit destination list or fall back to one primary storage.
- Uploads return and persist the stable object key, size, and SHA-256 metadata.
- Existing objects are never silently overwritten.
- Remote retention is paginated and restricted to valid Bqckup site prefixes.
- Cancellation stops remote work and produces a terminal cancelled run.
- Credentials and endpoints never appear in public output, JSON, history, logs, repository examples, or tests.
- Real inline credential files are rejected unless they are non-symlink files with mode `0600`.
- Default tests remain deterministic and network-independent.
- `make verify` and `sh scripts/check-docs.sh` pass from a clean checkout.
