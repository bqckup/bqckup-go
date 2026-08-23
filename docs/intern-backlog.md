# Intern feature backlog

Each milestone is an independently assignable pull request. The assignee must read `architecture.md`, `configuration-v2.md`, `development.md`, and `testing.md` before editing. Use red-green-refactor, keep secrets out of fixtures and output, update public docs with public contracts, and finish with `make verify` plus `sh scripts/check-docs.sh`.

Do not add placeholder commands or config that merely return “not implemented.”
The incremental engine work is delivered in M11–M14; the official Restic
binary remains an opt-in compatibility-test oracle, not a runtime adapter.

## M01 — MySQL exporter

**Status:** Delivered together with the PostgreSQL exporter; retain this section as its acceptance checklist.

**Objective:** Export one enabled MySQL database into a compressed, checksummed artifact that the existing runner can send to every destination.

**Prerequisites:** Foundation green; reviewer-approved exporter contract and process-executor boundary.

**In scope:** `internal/backup/database`, MySQL-specific validation, safe `mysqldump` invocation through `exec.CommandContext`, password environment handling, artifact naming, runner integration, examples and docs.

**Out of scope:** SQLite, schema/data restore, shell commands, and database credentials in arguments or tracked YAML.

**Acceptance:** Multiple named MySQL sources can run; cancellation kills the child process; incomplete dumps are removed; every destination receives the artifact; failure marks the run failed without retention.

**Required tests:** Argument/environment contract, missing binary, non-zero exit, cancellation, redaction, cleanup, multi-destination integration, config validation.

**Suggested commit:** `feat: add MySQL backup exporter`

## M02 — PostgreSQL exporter

**Status:** Delivered together with the MySQL exporter; retain this section as its acceptance checklist.

**Objective:** Add PostgreSQL dump artifacts using the same consumer-owned exporter contract.

**Prerequisites:** M01 exporter boundary merged, or an equivalent contract approved first.

**In scope:** PostgreSQL validation, `pg_dump` process adapter, controlled `PGPASSWORD` environment, compressed artifact, runner and documentation integration.

**Out of scope:** MySQL refactors unrelated to reuse, logical replication, restore, shell invocation.

**Acceptance:** Enabled PostgreSQL sources produce named checksum artifacts; failures and cancellation are terminal and redacted; other source types remain unaffected.

**Required tests:** Executable/arguments/environment, missing dependency, exit failure, cancellation, cleanup, secret redaction, real disposable integration when CI support is approved.

**Suggested commit:** `feat: add PostgreSQL backup exporter`

## M03 — SQLite source exporter

**Objective:** Create a consistent backup artifact from an application-owned SQLite database without copying a live database unsafely.

**Prerequisites:** Exporter contract merged; backup strategy approved (`VACUUM INTO` or SQLite backup API).

**In scope:** SQLite source validation, consistent snapshot creation, compression/checksum, lock/busy handling, runner integration.

**Out of scope:** The internal Bqckup state database, restore, arbitrary SQL execution.

**Acceptance:** A live WAL-mode test database produces a readable consistent snapshot; source and output paths cannot collide; cancellation and partial failures clean up.

**Required tests:** WAL snapshot consistency, busy timeout, missing source, invalid output, cancellation, cleanup, artifact metadata.

**Suggested commit:** `feat: add SQLite source exporter`

## M04 — S3-compatible storage adapter

**Status:** Delivered in the S3/R2 storage change; retain this section as its acceptance checklist.

**Objective:** Store verified artifacts in an S3-compatible service using AWS SDK for Go v2.

**Prerequisites:** Storage contract and local contract tests understood.

**In scope:** S3/R2 adapter, endpoint/region/bucket/prefix validation, protected inline runtime credentials, context-aware conditional upload, checksum metadata, object key safety, contract tests.

**Out of scope:** Remote HTTP credential retrieval, S3 retention, restore, multipart tuning beyond measured need.

**Acceptance:** Upload returns stable key/size/checksum; existing objects are not silently overwritten unless the approved contract explicitly permits identical content; cancellation aborts upload.

**Required tests:** Shared storage contract, fake SDK client, endpoint validation, cancellation, provider error redaction, optional disposable S3-compatible integration.

**Suggested commit:** `feat: add S3-compatible storage adapter`

## M05 — Environment-backed S3 credentials (deferred)

**Objective:** Optional future design only. The approved current contract uses inline credentials in a non-symlink runtime storage file with mode `0600`.

**Status:** Not ready for implementation assignment. A separate design review and explicit approval to expand the credential model are required before scope, acceptance criteria, or tests are written.

## M06 — Remote HTTP credential provider

**Objective:** Support the legacy `remote_url` use case through a constrained provider whose URL is itself supplied by an environment variable.

**Prerequisites:** M04 merged; response schema, TLS policy, timeout, and refresh semantics approved in a short design note.

**In scope:** `credentials.source: remote`, `url_env`, HTTPS client with timeout, bounded response, strict JSON decoding, in-memory expiry handling, redacted errors.

**Out of scope:** URL literal in YAML, credential persistence, arbitrary headers/scripts, retries without limits.

**Acceptance:** Valid responses create short-lived SDK credentials; non-HTTPS endpoints are rejected except explicitly isolated tests; response bodies and URLs never appear in output/history.

**Required tests:** Timeout, cancellation, status code, oversized/malformed response, expiry, missing URL env, URL and credential redaction.

**Suggested commit:** `feat: add remote S3 credential provider`

## M07 — S3 retention

**Status:** Delivered with prefix-scoped pagination and bounded deletion; retain this section as its acceptance checklist.

**Objective:** Reuse retention selection semantics for successful S3 backup sets.

**Prerequisites:** M04 merged; list/delete behavior and pagination available.

**In scope:** Listing only known Bqckup prefixes, timestamp parsing, paginated discovery, oldest-first deletion, empty-prefix safety, integration with successful runs.

**Out of scope:** Bucket-wide cleanup, lifecycle-rule management, deletion after a failed run, restore.

**Acceptance:** Exactly `keep_last` valid sets remain; unrelated/invalid objects remain untouched; the first delete error stops the operation and is categorized as storage.

**Required tests:** Pagination, invalid timestamps, foreign prefixes, empty result, partial delete failure, cancellation, successful-run-only runner behavior.

**Suggested commit:** `feat: apply retention to S3 backups`

## M08 — Doctor command

**Objective:** Add read-only dependency and connectivity diagnostics with text and JSON output.

**Prerequisites:** Checks are specified for every implemented source/destination type.

**In scope:** `bqckup doctor [--site <name>]`, config check, writable state/temp/lock paths, exporter binary discovery, non-mutating destination probes, categorized results.

**Out of scope:** Repair, creating buckets, running backups, revealing provider details or secrets.

**Acceptance:** Each check has stable name/status/message; JSON has no ANSI; missing dependency maps to exit code 3; checks honor context cancellation.

**Required tests:** Healthy/unhealthy matrices, site filter, JSON schema, stderr routing, exit mapping, redaction, no backup/history mutation.

**Suggested commit:** `feat: add backup preflight doctor command`

## M09 — Legacy YAML migration command

**Objective:** Convert a copied Python configuration tree into a separate schema-v2 destination with an explicit report.

**Prerequisites:** `migration-from-python.md` mappings reviewed against sanitized legacy fixtures.

**In scope:** `bqckup config migrate`, `.cnf`/`.yml` parsing, site/storage mapping, secret-to-env-name prompts or report items, dry run, no-overwrite output, unsupported-setting report.

**Out of scope:** Editing live legacy files, copying secrets, enabling unavailable MySQL/S3 features, converting Rustic to archive or Restic.

**Acceptance:** Deterministic output; original tree unchanged; existing destination never overwritten; Rustic/incremental and unsupported fields are prominently reported; generated YAML validates only when all referenced implemented features are available.

**Required tests:** Sanitized four-site fixture, dry run, collisions, inline-secret detection without value leakage, unknown field, partial input, report stability.

**Suggested commit:** `feat: add legacy configuration migration`

## M10 — Packaging and release automation

**Objective:** Produce reproducible Linux CLI artifacts and checksums from tagged releases.

**Prerequisites:** CI green; supported Linux architectures and CGO policy approved.

**In scope:** version/commit/date ldflags, GitHub Actions release workflow, archive naming, SHA-256 manifest, installation documentation, smoke test of packaged binary.

**Out of scope:** Automatic production deployment, package-manager repositories, signing keys without an approved custody process.

**Acceptance:** A test tag builds documented targets; `bqckup version` contains injected metadata; artifacts start on a clean supported host; checksums verify.

**Required tests:** Build matrix, version output, archive contents, checksum verification, failure on dirty or missing tag metadata where applicable.

**Suggested commit:** `build: add reproducible release artifacts`

## M11 — Restic design cycle (later)

**Status:** Delivered. The final runtime decision is one in-tree pure-Go
engine; the transitional process adapter and `incremental.engine` selector
were removed after M12–M14 completed.

**Objective:** Produce an approved design before any Restic dependency, command, package, or config is added.

**Prerequisites:** Product decision on backup and restore semantics; current Restic documentation reviewed; threat model for repository passwords and remote credentials.

**In scope for the first PR:** Design only—adapter boundary, archive-mode compatibility, repository initialization, backup, snapshots, retention, locking, cancellation, credential flow, restore safety, migration, test strategy.

**Out of scope:** Implementation during the design PR, Rustic compatibility claims, silent replacement of archive mode, passwords in YAML, destructive restore defaults.

**Design artifacts:** `docs/superpowers/specs/2026-08-20-restic-engine-phase1-design.md` (historical design), format verification and decision notes in `docs/superpowers/notes/`, and the final runtime decision in `docs/restic-engine-planning.md`.

**Acceptance:** Maintainer approves the design and splits implementation into independently testable follow-up milestones. Archive mode remains supported. Restore requires explicit destination and overwrite safeguards.

**Required tests:** None for the design PR; each approved implementation milestone must define contract, integration, cancellation, redaction, and restore-safety tests before coding.

**Suggested commit:** `docs: design optional Restic integration`

## M12 — Builtin engine S3/R2 backend (L3)

**Status:** Delivered; retain this section as its acceptance checklist. S3/R2
backend shipped in `internal/engine/restic/backend/s3.go`; incremental mode
serves s3/r2 destinations directly; credentials flow in memory only.

**Roadmap:** `tasks/plan-l3-l4-l2.md`.

**Objective:** Implement the engine `Backend` interface over S3/R2 so the built-in incremental engine serves cloud destinations too.

**Prerequisites:** M11 engine green; L3 design note approved (default layout only, reuse s3compat patterns).

**In scope:** `internal/engine/restic/backend/s3.go` (Handle-based Save/Load with offset+length/Stat/List/Remove), restic `default` layout as object key prefixes, AWS SDK v2 config + transfermanager upload pattern reused from `internal/storage/s3compat`, network-free fake-SDK tests, optional disposable MinIO integration, config validation change: `builtin` becomes valid for s3/r2 destinations, app wiring passes credentials in memory only.

**Out of scope:** `s3legacy` layout, multipart tuning beyond the existing transfermanager usage, automatic migration of format-v1 repositories, changing the runtime credential file contract (0600 non-symlink), S3 lifecycle rules.

**Acceptance:** `restic_compat` suite passes against a MinIO-backed repository (check/snapshots/restore); builtin backup to an S3 destination produces identical dedup to local; no credential appears in logs/history/output; cancellation aborts uploads.

**Required tests:** Backend contract tests via fake SDK (save/load offset+length/list/remove/stat), layout key mapping, config validation, redaction, cancellation, optional MinIO integration.

**Suggested commit:** `feat: add S3/R2 backend to the builtin restic engine`

## M13 — Builtin engine lock management (L4)

**Status:** Delivered; retain this section as its acceptance checklist.
Restic-compatible locks shipped in `internal/engine/restic/lock`
(encrypted lock blobs, 30-minute staleness, `bqckup backup unlock`),
verified against the official restic 0.19.1 binary in both directions.

**Roadmap:** `tasks/plan-l3-l4-l2.md`.

**Objective:** Restic-compatible locks so concurrent backups and prune cannot corrupt the repository; locks respected in both directions with the official binary.

**Prerequisites:** M12 merged (locks must work on local and S3 backends).

**In scope:** Lock file format identical to restic (JSON: time, exclusive, hostname, username, pid, uid, gid), exclusive lock for backup and prune, non-exclusive for listing, auto-removal of stale non-exclusive locks older than 30 minutes, stale exclusive lock → clear error suggesting unlock, real `Unlock` implementation (replaces the current no-op), honoring locks created by the official restic binary.

**Out of scope:** Distributed lock services, silently removing stale exclusive locks. (Lock refresh loop: restic renews ~every 5 minutes; whether bqckup matches this or accepts stale-exclusive risk on long runs is decided in the L4-D1 design review.)

**Acceptance:** Two concurrent builtin backups: the second fails cleanly with a lock error; the official restic binary blocks on our exclusive lock and vice versa; stale non-exclusive locks auto-clean; `restic check` passes.

**Required tests:** Lock format parse/serialize, staleness math, concurrent backup behavior, cross-tool lock respect (compat tag), unlock path, redaction.

**Suggested commit:** `feat: add restic-compatible locking to the builtin engine`

## M14 — Forget + prune (L2)

**Status:** Delivered; retain this section as its acceptance checklist.
`repository.ForgetAndPrune` ships mark-and-sweep prune (no repack): new
index written before pack deletion, orphaned packs cleaned, reclaimed
bytes reported in run output (`reclaimed_bytes` in JSON, `reclaimed …` in
text). Verified against the official restic 0.19.1 binary: `restic check`
green after prune.

**Roadmap:** `tasks/plan-l3-l4-l2.md`.

**Objective:** Deleting old snapshots actually reclaims pack space via mark-and-sweep prune (no repack).

**Prerequisites:** M13 merged (prune runs under an exclusive lock); M11 retention semantics unchanged.

**In scope:** Forget by site tag with `keep_last` (replacing snapshot-file-only deletion), prune mark/sweep (keep blobs referenced by kept snapshots, delete unreferenced packs, rewrite the index), `restic check` compatibility after prune, space-reclaimed reporting in run output.

**Out of scope:** Repack (recorded as a future option with a size threshold), S3 lifecycle rules, re-encryption, data compression changes.

**Acceptance:** After retention, total pack bytes shrink; `restic check` passes; an interrupted prune leaves the previous state valid (new index written before pack deletion); retention never runs after a failed export or storage operation (existing repo rule).

**Required tests:** Blob reachability graph, unreferenced pack deletion, partially-referenced packs survive, index consistency, compat `restic check` after prune, cancellation.

**Suggested commit:** `feat: reclaim space with mark-and-sweep prune`

## Mentor review checklist

- Assignment is exactly one milestone with prerequisites satisfied.
- First meaningful commit or PR evidence includes a failing test.
- Domain/use-case packages do not import Cobra, Viper, GORM, or SDK concrete clients.
- Context, cancellation, cleanup, and restrictive permissions are covered.
- Secret values cannot reach config examples, logs, stderr, JSON, history, or subprocess arguments.
- Failure does not trigger retention or delete a prior successful set.
- Public config/CLI changes include strict validation, examples, migration notes, and docs.
- `make verify` and `sh scripts/check-docs.sh` pass from a clean checkout.
