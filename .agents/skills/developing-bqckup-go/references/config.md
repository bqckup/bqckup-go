# Strict configuration rules

Before changing YAML, inspect `internal/config/types.go`, `load.go`,
`validate.go`, their tests, `internal/cli/init.go`, `configs/`, and the user
guide.

1. Give each field exact typed `mapstructure` and `yaml` tags.
2. Preserve a fresh Viper instance per file and strict `UnmarshalExact`.
   Unknown fields must fail.
3. Define applicable types, required or optional behavior, defaults,
   normalization, validation, runtime effect, and security sensitivity.
4. Do not accept a field until its runtime behavior exists. Accepted but unused
   configuration is a broken public contract.
5. The `version` field is optional. Compatible optional additions may be
   introduced without a migration; changed meanings, new required fields, or
   incompatible defaults need an explicit migration decision.
6. Local storage paths and enabled file-source includes are absolute. S3/R2
   prefixes are safe relative object prefixes. R2 uses `region: auto` and
   HTTPS; only loopback custom endpoints may use HTTP.
7. At most one storage is primary. Every enabled site needs a destination,
   explicitly or through that primary storage.
8. Storage credentials live in `config/storages.yaml`; database passwords live
   in site YAML. Notification credentials and URLs live in root `bqckup.yaml`.
   Any credential-bearing file must be regular, non-symlink, and mode `0600`.
   Errors must not reveal values.
9. Incremental repository passwords are literal values in `incremental.password`.
   The site YAML must be regular, non-symlink, and mode `0600`; never log them.
10. Update structs, strict-load and validation tests, runtime behavior,
    `configs/`, initialization templates, `README.md`, and `USER-GUIDE.md`
    together when their public contract changes.
11. YAML values are authoritative. Do not resolve config values through
    environment variables and do not add app-field environment overrides.
    `BQCKUP_CONFIG_DIR` only selects the configuration directory when the CLI
    flag is omitted.
12. `server_id` is the global storage namespace and must satisfy `SafeName`
    when set. New examples use it so full and incremental backups share the
    `bqckup/<server_id>/<site>/` hierarchy.
13. `credentials.source: remote` accepts a literal absolute provider URL.
    Require HTTPS except loopback HTTP, retain provider responses only in
    memory, and redact URLs, response bodies, and returned credentials.
14. Notification channels are `smtp`, `webhook`, or `discord`. Routes accept
    `all`, `backup_failed`, `backup_cancelled`, and `backup_no_change`;
    successful runs remain silent. Enforce per-channel fields and URL safety.
15. `minimum_interval` defaults to `24h` and `keep_last` defaults to `7`; both
    must remain validated before reaching orchestration.

Older binaries reject unknown fields. Document upgrade ordering for optional
schema additions.
