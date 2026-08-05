# Migration from the Python application

The Go application intentionally does not parse the Python runtime schema. Migration is a controlled conversion into schema v2, not an in-place upgrade.

## Inspected legacy layout

The legacy repository and supplied runtime configuration use a root file, one storage file, and one file per site. The useful mapping is:

| Python concept | Schema v2 location |
|---|---|
| root/application settings | `bqckup.yaml` → `app` |
| named storages | `config/storages.yaml` → `storages` |
| one site per YAML | `sites/<name>.yaml` → `site` |
| backup interval | `site.policy.minimum_interval` |
| retention count | `site.policy.keep_last` |
| database definitions | `site.sources.databases` |
| file paths | `site.sources.files.include` and `exclude` |

There was no separate, reliable `jobs.d` contract in the supplied configuration tree. Schema v2 therefore does not introduce `jobs.d`; each `sites/*.yaml` file is the complete job definition for one site.

## Intentional changes

- extensions become `.yaml`;
- root and site documents default to schema version 2; an explicit `version` is optional and must be `2`; the storage document is unversioned;
- the site filename must equal the normalized site name;
- mutable SQLite state, locks, and temporary files move outside the config tree;
- intervals are durations rather than implicit scheduling behavior;
- storage references are explicit lists;
- secret values remain only in protected runtime files and are never copied into tracked output;
- unsupported flags produce a migration report instead of a silent approximation.

The inspected sites primarily use MySQL, S3-compatible storage, remote URL credentials, daily intervals, retention of seven, and in some cases Rustic incremental behavior. Those production configs still require a schema-v2 rewrite and binary/storage validation before production use.

## Safe migration sequence

1. Inventory site names, enabled sources, destinations, interval, and retention without copying secret values into tickets or logs.
2. Create a new schema-v2 tree in a separate directory.
3. Keep any inline database password only in the runtime site file and protect it with mode `0600`.
4. Keep unsupported database engines and providers disabled until their corresponding milestone is released.
5. Map file includes/excludes to absolute paths and test them against a disposable local destination.
6. Run `config validate`, then a forced test backup.
7. Compare archive contents, checksum, history, and retention behavior.
8. Switch production only after the required exporter and storage milestones pass their own acceptance checks.

Rustic settings are not converted. The future migration command must flag them as unsupported. Restic is a separate later design cycle and must not be inferred as a drop-in replacement during migration.

Never copy the supplied credential URL or inline passwords into repository examples, fixtures, documentation, commits, or issue descriptions.
