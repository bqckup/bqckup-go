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
   in site YAML. Any credential-bearing file must be regular, non-symlink, and
   mode `0600`. Errors must not reveal values.
9. Incremental repository passwords are values of the environment variable
   named by `incremental.password`; they are never stored in YAML.
10. Update structs, strict-load and validation tests, runtime behavior,
    `configs/`, initialization templates, `README.md`, and `USER-GUIDE.md`
    together when their public contract changes.

Older binaries reject unknown fields. Document upgrade ordering for optional
schema additions.
