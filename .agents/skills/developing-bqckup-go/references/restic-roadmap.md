# Restic roadmap gate

Restic is a later design cycle, not an implementation task in the foundation.

Before any code or schema change:

1. Review current official Restic documentation.
2. Obtain product decisions for archive compatibility, direct-source behavior, repository ownership and initialization, supported backends, binary/version policy, snapshots/history, locking, retention/forget/prune, cancellation, credentials, migration, and restore.
3. Threat-model repository passwords and backend credentials. YAML may contain only environment-variable names.
4. Write a design-only PR with an adapter boundary and network-free test strategy.
5. Preserve the existing archive workflow as a supported mode.
6. Require explicit restore destinations and safe no-overwrite behavior.
7. Get maintainer approval and split implementation into independent milestones.

During the design cycle, do not modify `go.mod`, Go packages, commands, schema-v2 structs, or examples for Restic. Do not add placeholders. Never silently translate legacy Rustic settings into Restic or archive behavior.
