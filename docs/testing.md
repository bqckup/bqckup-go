# Testing strategy

All feature and bug-fix work uses red-green-refactor. Tests should prove observable behavior rather than private implementation details.

## Layers

- Unit: validation, error mapping, retention selection, key safety, interval and status decisions.
- Contract: storage/exporter behavior that every adapter must satisfy.
- Integration: temporary SQLite databases, real filesystem archives, permissions, locks, cancellation, and cleanup.
- CLI: construct Cobra commands with in-memory stdout/stderr and parse JSON output.
- End to end: disposable schema-v2 config, source tree, local destination, backup run, and history query.

External database tools use fake process executors in unit tests. Verify executable, argument slice, environment key names, cancellation, exit handling, and redaction without contacting a production service. S3/R2 unit tests use adapter-owned fakes. Opt-in integration tests may use a disposable service or private runtime config; default checks never require network access or credentials.

Database exporter tests verify `mysqldump`/`pg_dump`, `MYSQL_PWD`/`PGPASSWORD`, gzip output, checksums, mode `0600`, cancellation cleanup, and redacted process failures. Runtime passwords are never placed in tracked examples.

Run the live smoke test only with a private mode-`0600` config and non-secret selectors:

```bash
BQCKUP_S3_INTEGRATION_CONFIG=/private/config \
BQCKUP_S3_INTEGRATION_STORAGE=testing \
go test -tags=integration ./internal/storage/s3compat -run TestDisposableS3CompatibleStorage -count=1 -v
```

The native incremental engine has a separate end-to-end S3-compatible smoke
test. It initializes a unique Restic-format repository, creates two dummy
snapshots with excludes, verifies listing, applies retention, and removes the
repository afterward:

```bash
BQCKUP_S3_INTEGRATION_CONFIG=/private/config \
BQCKUP_S3_INTEGRATION_STORAGE=testing \
go test -tags=integration ./internal/engine/restic/facade \
  -run TestDisposableIncrementalBackupS3Compatible -count=1 -v
```

Set `BQCKUP_S3_INTEGRATION_KEEP=1` only when the isolated dummy repository
should remain in the bucket for manual inspection. In that case,
`BQCKUP_RESTIC_INTEGRATION_PASSWORD` is also required so the retained
repository remains usable. The test logs its unique, non-secret object prefix.
Never place storage credentials directly in a test command.

## Required checks

```bash
make verify
sh scripts/check-docs.sh
```

`make verify` checks formatting, runs `go vet`, executes the entire suite with the race detector, and builds the CLI. Tests must not need a network, fixed host path, real credential, production config, or a particular execution order.

## Minimum cases for new adapters

- valid success path and metadata;
- invalid configuration and missing dependency;
- context cancellation and process termination;
- partial output cleanup;
- redaction of secrets and provider responses;
- multiple destinations/all-required behavior where applicable;
- deterministic test names and temporary resources;
- no change to previously successful backup sets after failure.

Use `t.TempDir`, fixed injected clocks, and fakes only at external boundaries. A bug fix starts with a regression test that fails for the actual defect.
