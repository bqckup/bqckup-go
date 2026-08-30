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

**Status:** Delivered (2026-08-26), then simplified in v0.0.5. Issue #21. The
provider URL is stored directly in `credentials.url`; returned storage settings
remain in memory only.

**Objective:** Support the legacy `remote_url` use case through a constrained provider whose URL is supplied directly by protected storage YAML.

**Prerequisites:** M04 merged; response schema, TLS policy, timeout, and refresh semantics approved in a short design note.

**In scope:** `credentials.source: remote`, literal `credentials.url`, HTTPS
client with timeout, bounded response, strict JSON decoding,
in-memory storage settings, redacted errors.

**Out of scope:** credential persistence, arbitrary headers/scripts, retries without limits.

**Acceptance:** Valid responses create short-lived SDK credentials; non-HTTPS endpoints are rejected except explicitly isolated tests; response bodies and URLs never appear in output/history.

**Required tests:** Timeout, cancellation, status code, oversized/malformed
response, missing URL, URL and credential redaction.

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

**Status:** Delivered (issue #16: database and storage connectivity probes). Retain this section as its acceptance checklist.

**Objective:** Add read-only dependency and connectivity diagnostics with text and JSON output.

**Prerequisites:** Checks are specified for every implemented source/destination type.

**In scope:** `bqckup doctor [--site <name>]`, config check, writable state/temp/lock paths, exporter binary discovery, non-mutating destination probes, categorized results.

**Out of scope:** Repair, creating buckets, running backups, revealing provider details or secrets.

**Acceptance:**
- [x] Each check has stable name/status/message; database checks are `database:<site>:<source>`, storage checks `storage:<storage>`.
- [x] JSON has no ANSI; text and JSON output stay identical in shape to the original doctor.
- [x] Missing dependency maps to exit code 3; `--site` unknown or disabled maps to exit code 2.
- [x] Checks honor context cancellation; every connectivity probe runs under a 10 s timeout child context.
- [x] Probes are non-mutating: DB probe uses `--no-data`/`--schema-only` with stdout discarded; local probe creates and removes one temp file; S3/R2 probe lists with `MaxKeys=1`. Known ceiling: S3 probe verifies list access, not `PutObject`.
- [x] Remote storages (`credentials.source: remote`) are resolved per-storage before probing; provider or validation failures surface as failing `storage:<name>` checks.

**Required tests:**
- [x] Healthy/unhealthy matrices (unit fakes in `internal/doctor`, no real connections).
- [x] Site filter (unit + CLI exit-code mapping).
- [x] JSON schema (CLI fixture asserts JSON field names and no ANSI bytes).
- [x] stderr routing (errors flow through the standard `cli.Execute` path with `SilenceErrors`).
- [x] Exit mapping (2 invalid input, 3 preflight).
- [x] Redaction (probe error codes only; no password/endpoint strings in any check message).
- [x] No backup/history mutation (doctor never opens the history database).

**Suggested commit:** `feat: add database and storage probes to doctor`

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

## M15 — Remote storage listing

**Status:** Delivered; retain this section as its acceptance checklist. See `docs/superpowers/specs/2026-08-24-remote-storage-listing.md` and `docs/superpowers/plans/2026-08-24-remote-storage-listing-plan.md`.

**Objective:** `bqckup storage list <destination> --site <site>` shows the live contents of one remote destination for one site: archive artifacts for `full` mode, restic snapshots for `incremental` mode. Port of the legacy `get-list` command.

**Prerequisites:** M04 and M07 delivered (S3 adapter, prefix-scoped listing and pagination); facade and lock package from M12/M13.

**In scope:** one `storage list` subcommand, `storage.Artifact` value type, `s3compat.Store.ListArtifacts`, `facade.Engine.ListSnapshots` with a non-exclusive lock, a consumer-owned lister in `internal/backup`, app wiring, text and JSON rendering, README and `CONTEXT.md` updates.

**Out of scope:** local destination listing (use `history list --details`), restore, link generation, Python-era object layouts, listing raw restic repository objects, `--full-id`, any new config field or dependency.

**Acceptance:** text tables match the spec; `--output json` emits the two spec schemas with `[]` on empty results; local or unknown destination fails with exit 2 and a pointer to `history`; storage failures exit 4 redacted; listing mutates nothing on the remote; `make verify` and `sh scripts/check-docs.sh` pass.

**Required tests:** S3 fake-client pagination/filter/cancellation/redaction, facade snapshot listing with lock lifecycle, use-case mode branching and error mapping, CLI table and JSON shape, exit codes.

**Suggested commit:** `feat: add remote storage listing command`

## M16 — Download link for remote artifacts

**Status:** Delivered; retain this section as its acceptance checklist. See `docs/superpowers/specs/2026-08-24-download-link.md` and `docs/superpowers/plans/2026-08-24-download-link-plan.md`.

**Objective:** `bqckup storage link <destination> --key <key> --expires <n>h` prints a temporary signed download URL for one archive artifact of a full-mode site on a remote destination. Port of the legacy web UI's `get_download_link` endpoint.

**Prerequisites:** M04 and M15 delivered (S3 adapter, `storage list` output format the key is copied from).

**In scope:** one `storage link` subcommand, `storage.DownloadLink` value type, `s3compat.Store.PresignLink` (HEAD existence check plus client-side presign with `attachment` content disposition), `local.Store.LocalPath` for the local-destination error message, a consumer-owned linker in `internal/backup`, app wiring that parses the site from the key, text and JSON rendering, README and `CONTEXT.md` updates.

**Out of scope:** linking restic repository blobs (incremental sites fail with a pointer to restore), local destinations (no URL exists; the error shows the local path), any config field or dependency change, history writes, restore.

**Acceptance:** a valid key produces a signed URL on stdout that downloads the artifact as an attachment and expires after the requested whole-hour duration (1–24h, default 24h); missing objects exit 4 with a redacted message naming the key; the URL never appears in errors, logs, or history; the command writes nothing and touches the remote only with one HEAD.

**Required tests:** presign URL shape (signature, disposition, expiry), 404 mapping, redaction, cancellation, key-shape rejection, mode branching, local-path error, CLI flag validation and output split, exit codes.

**Suggested commit:** `feat: add download link for remote storage artifacts`

## M17 — Incremental snapshot listing

**Status:** Delivered (2026-08-26). See `docs/superpowers/specs/2026-08-26-backup-snapshots.md`.

**Objective:** `bqckup backup snapshots <site> --destination <name>` shows the live snapshots of one incremental site, read directly from the repository for local, S3, and R2 destinations. First half of issue #17; the legacy counterpart is `bqckup get-list <name>`.

**Prerequisites:** M12–M14 delivered (engine facade with `ListSnapshots` and non-exclusive listing locks), M15 delivered (`storage list` whose incremental output shapes this command reuses).

**In scope:** one `backup snapshots` subcommand, a new `backup.Lister` method that skips M15's remote-only assertion and reuses the existing snapshot listing path, app wiring for site/destination validation, text and JSON rendering reused from `storage list` (8-character IDs, no `--full-id`), README and guide updates, and one message change: `storage list` on a local destination of an incremental site points at `backup snapshots` instead of `history list --details`.

**Out of scope:** restore (second half of issue #17, reserved as M18 with guardrails already locked in the M11 design), any history behavior change beyond that message, `--full-id`, full-mode sites (config error pointing at `history list --details`), any new config field or dependency.

**Acceptance:** local and S3/R2 destinations list snapshots newest first; `--output json` emits the M15 incremental schema with `[]` on empty results; a `full`-mode site exits 2 pointing at `history list --details`; a missing password env exits 3; a broken repository exits 4 redacted; listing writes nothing to history and touches the repository only with the short-lived non-exclusive lock; `make verify` and `sh scripts/check-docs.sh` pass.

**Required tests:** use-case mode branching and error mapping (local destination succeeds, full mode rejected, password and engine failures mapped), app validation matrix (unknown/disabled site, unknown or unused destination), CLI flags and table/JSON shape, updated `storage list` local-rejection message, exit codes.

**Suggested commit:** `feat: add incremental snapshot listing command`

## M18 — Incremental snapshot restore

**Status:** In progress (2026-08-26). See `docs/superpowers/specs/2026-08-26-restore.md`.

**Objective:** `bqckup backup restore <site> --destination <name> --snapshot <id|latest> --target <path> [--force] [--quiet]` rebuilds the configured file paths of one snapshot into an explicit target directory (restic layout), from local, S3, or R2 repositories. Second half of issue #17; the legacy counterpart is `bqckup restore <site> --target <dir>`.

**Prerequisites:** M12–M15 delivered (engine facade, non-exclusive locks, index/pack/decrypt/zstd read path), M17 delivered (snapshot listing the restore resolves against).

**In scope:** an in-tree `internal/engine/restic/restorer` package (tree walk, blob loading, staging directory, conflict collection, single confirm callback, move into place), repository `LoadBlob`/`LoadTree`/`LoadSnapshot` exports, a facade `RestoreSnapshot` method, a new `backup.Restorer` use case (mode gate, password preflight, site-tag snapshot resolution, target preflight), app wiring sharing M17's validation matrix, the `backup restore` subcommand with `--destination --snapshot --target --force --quiet`, text and JSON summaries, README/guide/backlog/CONTEXT updates.

**Out of scope:** restoring full-mode sites (config error pointing at `history list --details`), paths no longer configured in the site, uid/gid and xattr restoration, hardlink grouping, special node types (skipped), history writes, any new config field or dependency.

**Acceptance:** byte-for-byte round trip with restic layout and symlinks/modes intact on all three destination types; conflicts prompt once on stderr with the exact list, a declined prompt exits 4 and writes nothing, `--force` skips the prompt, non-TTY conflicts without `--force` exit 3 naming `--force`; `latest` and ID prefixes resolve against the site tag only; skipped configured paths are reported, an empty intersection exits 4; cancellation removes staging and leaves the target untouched; `--output json` emits the summary schema, `--quiet` prints nothing on success; no credential ever appears; history unchanged; `make verify` and `sh scripts/check-docs.sh` pass.

**Required tests:** engine round trip (bytes, modes, symlinks, empty dirs, filtering, skipped paths, conflicts, abort, cancellation, missing blobs, special nodes), facade restore and lock lifecycle, use-case snapshot resolution and error mapping (site tag, prefixes, full-mode rejection, password and target preflights, confirm pass-through, redaction), app validation matrix, CLI flags/prompt/summary/exit codes.

**Suggested commit:** `feat: add incremental snapshot restore command`

## M19 — Human-readable archive backup-set layout

**Status:** Delivered (2026-08-26). Issue #20.

**Objective:** Make full-mode archive paths recognizable and sortable in local
and S3/R2 object browsers.

**Prerequisites:** M07 and M15 delivered (retention and remote listing consume
the archive backup-set prefix).

**In scope:** new UTC
`bqckup/<site>/DD-MMMM-YYYY/HH-mm-ss/<artifact>` keys with English
month names, file and database artifact paths, local and S3/R2 listing,
retention deletion, legacy flat-layout recognition, and canonical docs.

**Out of scope:** incremental/Restic repository layout, configuration changes,
artifact content, restore, or migration/renaming of existing objects.

**Acceptance:** new full-mode runs use the readable layout; write-once storage
rejects a duplicate site/second instead of overwriting it; listing and retention
recognize both new and legacy archive sets; delete validation cannot target a
date directory or wider prefix; `make verify` and `sh scripts/check-docs.sh`
pass.

**Required tests:** UTC/English formatting, parser strictness and legacy
compatibility, runner file/database keys, local and S3/R2 set discovery,
same-second overwrite rejection, and safe retention prefix validation.

**Suggested commit:** `feat: use readable archive backup paths`

## M20 — Backup summary command

**Status:** Planned (2026-08-27). Issue #14.

**Objective:** `bqckup backup summary [--site <name>]` prints a read-only
per-site report, text panel or JSON, built from the active configuration and
SQLite history. It never runs a backup and never reads a destination. The
legacy counterpart is `bqckup summary`.

**Prerequisites:** Delivered history recording and the `backup list`
composition pattern. No new schema, config field, or dependency.

**In scope:** every configured site including disabled ones, sorted by name;
status disabled/running/idle from the latest run; successful-run counters
with the logical dedup size rule of `history list`; destinations rendered
with storage type and primary marker; retention `keep last N`; JSON object
with `--site`, array without; `No backup sites configured.` for an empty
configuration.

**Out of scope:** any destination access, snapshot or repository stats for
incremental sites, Schedule/Next Backup rows, watch mode, stale running-row
cleanup (future doctor work), history schema changes.

**Acceptance:** text and JSON contracts as locked in
tasks/plan-backup-summary.md; unknown `--site` exits 2; disabled sites are
shown; orphan history runs are ignored; no credential appears in output;
`make verify` and `sh scripts/check-docs.sh` pass.

**Required tests:** pure aggregation tests (status semantics, logical dedup,
orphan runs, empty values, incremental sizes as recorded, primary marker,
filter, sorting, empty config), command tests (text panel for
successful/disabled/never-run sites, JSON object and array, exit code 2,
empty config message), doc gates.

**Suggested commit:** `feat: add backup summary command`

## M21 — Global notifications (SMTP, webhook, Discord)

**Status:** Planned (2026-08-29). Issue #15. Spec: `SPEC-notifications.md`
(project root); plan: `tasks/plan-notifications.md`.

**Objective:** `notifications:` in the root schema-v2 config: named
channels (`smtp`, `webhook`, `discord`) and routes mapping events
(`backup_failed`, `backup_cancelled`, `backup_no_change`) to channels
(successful runs stay silent). After a failed, cancelled, or unchanged run is
recorded in history, the runner notifies through every matching channel with
one shared sanitized payload including failure context (`last_successful_at`,
`failure_streak`). Delivery is best effort: a failing channel warns on stderr
and never changes run status or history. `bqckup config validate` checks
literal endpoints and protected-file permissions.

**Prerequisites:** Delivered history recording (`RunArtifacts` query). No
new dependencies (`net/smtp`, `net/http`), no history schema change,
`RunResult` JSON contract unchanged.

**In scope:** config types, strict decode and validation (per-type fields,
`*` references only, both-or-neither SMTP auth, route/channel
references); `internal/notify` package (dispatcher, shared payload with
distinct-source artifact aggregation, webhook, Discord embed, SMTP with
STARTTLS and implicit TLS on 465, PLAIN auth only over encrypted sessions);
runner hook after terminal `FinishRun` (including the success-path
`FinishRun` failure notifying `backup_failed`/`persistence`); app wiring;
`config validate` environment check; docs and example config.

**Out of scope:** notification persistence, dedupe, retry, cross-channel
fallback, parallel fan-out, monthly reports, an event for `skipped` runs,
and notifications for preflight failures (no run row exists). The legacy
`bqckup` notification model (flat channel CSV, no routes, webhook→Discord
fallback) is not ported; the issue is the contract.

**Acceptance:** every invalid notification form fails validation with a
specific error; a recorded terminal run delivers the exact spec payload to
every channel of every matching route; failed/cancelled payloads carry
redacted `error_category`/`error_message`; skipped and preflight runs send
nothing; a failing channel does not stop others and does not change the run
result or history; `config validate` names each missing environment
variable while `bqckup backup` still runs; email/webhook/Discord renders
contain no credential, endpoint, source path, or raw error; `make verify`
and `sh scripts/check-docs.sh` pass; no new dependencies in `go.mod`.

**Required tests:** config validation table (every rule), strict-decode
rejection of plaintext credentials, aggregation (distinct source counting,
incremental recorded size), payload marshal schema, dispatch (fan-out,
unmatched event, dedupe across routes, one-failure-doesn't-stop), webhook
and Discord via `httptest` (method, content type, exact body, non-2xx,
network error, timeout), SMTP against a loopback fake (plain, STARTTLS with
self-signed injected roots, implicit TLS, no-STARTTLS auth refusal, no
credentials in body), runner hook (per-outcome event, category and redacted
message, nothing for skipped/preflight, `RunResult` unchanged, notifier
error is a warning), app wiring (dispatcher from config, nil without), CLI
(`config validate` env flags, backup still runs with unset env), doc gates.

**Suggested commit:** `feat: add global notifications (SMTP, webhook, Discord)`

## Mentor review checklist

- Assignment is exactly one milestone with prerequisites satisfied.
- First meaningful commit or PR evidence includes a failing test.
- Domain/use-case packages do not import Cobra, Viper, GORM, or SDK concrete clients.
- Context, cancellation, cleanup, and restrictive permissions are covered.
- Secret values cannot reach config examples, logs, stderr, JSON, history, or subprocess arguments.
- Failure does not trigger retention or delete a prior successful set.
- Public config/CLI changes include strict validation, examples, migration notes, and docs.
- `make verify` and `sh scripts/check-docs.sh` pass from a clean checkout.
