# Changelog

All notable changes to Bqckup are documented in this file.

## v0.0.5

- Added global `server_id` namespacing under `bqckup/<server_id>/<site>/`.
- Renamed internal incremental packages from `restic` to `incremental` while
  preserving official Restic repository compatibility.
- Rebuild releases on every push to `main` by recreating the `v0.0.5` tag.

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
