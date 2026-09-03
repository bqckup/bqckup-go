# Changelog

All notable changes to Bqckup are documented in this file.

## v0.0.7

- Reuse unchanged files and directory trees from the previous matching
  incremental snapshot, reducing repeated chunk reads while preserving parent
  snapshot metadata and Restic-compatible output.
- Calculate SHA-256 and compressed package size while writing full file archives
  and database exports, avoiding a second read of the completed package.

## v0.0.6

- Add `bqckup update` to download and SHA-256 verify the selected Linux
  release before atomically replacing the installed binary.
- Support multiple full-backup source paths with descriptive archive roots,
  including paths such as `etc/crowdsec`, `var/lib/crowdsec`, and
  `usr/lib/crowdsec`.
- Store full backup packages in sortable date folders using
  `<HH-mm-ss>-<run_id>-<package>.gz` names, with multiple database dumps in
  the same run grouped by their shared time and run ID.
- Update S3/local listing and retention to discover and remove flat package
  runs as one unit.

## v0.0.5

- Show immediate per-site start/completion progress, an interactive loading
  spinner, and a redirected-output heartbeat for `backup run` while keeping
  JSON output clean for automation.
- Simplify text summary headings to the site name without repeating the
  `Backup Summary for` command context.
- Use ASCII-only status markers (`[OK]`, `[FAIL]`, `[WARN]`) across text CLI
  output for reliable display in minimal terminals.
- Add configurable application logging with `app.log_file` and level filtering
  for `debug`, `info`, `warn`, and `error`.
- Treat `no_change` backup results as successful informational outcomes with
  exit code 0 instead of exit status 5.
- Add usage and example hints to invalid command input errors.
- Format CLI input errors as readable blocks with `[FAIL]`, `Usage`, and
  `Example` sections.
- Derive command usage and examples from Cobra command metadata to keep CLI
  help and validation guidance synchronized.
- Show incremental database exports separately from file snapshots in storage
  listings, including structured JSON when both kinds are present.
- Resolve `storage link` site names from the current
  `bqckup/<server_id>/<site>/...` object-key layout and clarify that package
  keys must be supplied with `--key`.
- Format text download-link metadata with a clear heading and aligned labels.
- Use one compact UTC format (`02 Jan 2006 15:04 UTC`) for human-readable CLI
  timestamps; structured JSON retains machine-readable RFC3339 timestamps.
- Add an enabled `failure-test` site example with a missing source path for
  testing backup failure notifications and history.
- Add a Python converter for legacy `storages.yml`, site files, and
  `bqckup.cnf` configuration trees.
- Configure the converter and installer to use `/var/log/bqckup/bqckup.log`.
- Let the converter auto-detect `/etc/bqckup_old` as input and `/etc/bqckup`
  as output when no paths are supplied.
- Detect legacy files still located in `/etc/bqckup` and use
  `/etc/bqckup_old` as the preserved source before writing the converted
  config back to `/etc/bqckup`.
- Report malformed legacy site files with their path instead of a Python
  traceback.
- Skip existing schema-v2 site templates found alongside legacy site files.
- Normalize legacy database type `postgresql` to the v2 `postgres` engine.
- Accept uppercase letters in site, storage, database, and notification names
  for compatibility with legacy configurations.
- Make MySQL exports ignore conflicting client credentials in system option
  files and use the password supplied by Bqckup configuration.
- Allow `--force` to regenerate an existing `/etc/bqckup` output from the
  preserved `/etc/bqckup_old` source.
- Remove duplicate output-path validation so `--force` is handled consistently.
- Document the migration layout using `/etc/bqckup_old` as the preserved
  legacy tree and `/etc/bqckup` as the generated active tree.
- Show each batch failure beside its site and replace the duplicated trailing
  error detail with one concise batch-failure message.
- Shorten run IDs in text output and wrap long failure reasons for narrow
  terminals; JSON keeps the complete run IDs.

- Added global `server_id` namespacing under `bqckup/<server_id>/<site>/`.
- Renamed internal incremental packages from `restic` to `incremental` while
  preserving official Restic repository compatibility.
- Rebuild releases on every push to `main` by recreating the `v0.0.5` tag.
- Accept notification credentials and webhook URLs directly in the protected
  root config, with strict URL checks and mandatory `0600` permissions.
- Read incremental repository passwords and remote storage provider URLs
  directly from protected YAML instead of resolving environment variables.
- Make YAML values authoritative by removing environment overrides for root
  application paths and log level.

## v0.0.4

- Added global completion notifications through SMTP email, generic webhooks,
  and Discord webhooks, configured from the root `bqckup.yaml`.
- Improved run summaries and history details with notification delivery status
  and richer per-destination results.
- Hardened storage handling for local and S3-compatible destinations, including
  clearer backup artifact paths and safer destination result tracking.
- Updated configuration, architecture, and README documentation for notification
  settings and environment-backed secrets.

## v0.0.3

- Incremental backups, improved repository handling and history, plus releases
  created automatically from `main`.

## v0.0.2

- File and database backups through the command line with local, S3, and R2
  storage.

## v0.0.1

- First Linux release with a command-line installer.
