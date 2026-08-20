# Restic Engine — Threat Model (Passwords & Credentials)

**Date:** 2026-08-20
**Status:** Draft — part of the Phase 0 design PR
**Scope:** the pure-Go in-tree engine (`internal/engine/restic/`), L1, local
repositories. Matches the spec §1.6 Secret Safety invariant.
**Model:** a local attacker who can read the filesystem or process memory of
the backup host, and an accidental-leak threat through logs, errors, history,
and subprocess arguments. Remote attackers are out of scope for L1 (local
repos only).

## Asset inventory

| # | Asset | Where it lives | Who can read it | Leak impact | Mitigation |
| :--- | :--- | :--- | :--- | :--- | :--- |
| 1 | User password | Process environment (`RESTIC_PASSWORD` / the env var named by `password_env`) and process memory (transient) | Same-user processes with /proc access; accidental log leakage | Full repo compromise: decrypts the key file, then everything | Read from env only, never from YAML. Held in memory only while deriving/verifying; zeroed after use. Never logged, never in errors, never in history, never in argv. YAML may contain only the env var NAME. |
| 2 | Master key (32B AES + 16B k + 16B r) | Process memory only, after key-file decryption | Memory-forensic attacker; core dumps | Decrypts/forges every blob, index, tree, snapshot | Never written to disk in plaintext. Zeroed when the repository closes. No core-dump-friendly global caching; kept on the repository struct, not package globals. |
| 3 | Key file (`keys/<64hex>`) | Repo directory on disk | Anyone who can read the repo dir | Encrypted with scrypt(KDF from password); offline brute force becomes feasible if the password is weak | File mode 0600, repo dirs 0700. scrypt params written into the key file (N=65536, r=8, p=1). Filename is a SHA-256 of the encrypted bytes — leaks nothing. |
| 4 | Repo data (config, packs, index, snapshots) | Repo directory on disk | Anyone who can read the repo dir | Same as #3: ciphertext without the master key is worthless | All files AES-256-CTR + Poly1305-AES authenticated. Files 0600, dirs 0700. Atomic writes: a crash never leaves a readable partial file at a final path. |
| 5 | Error messages / logs / history | stderr, `bqckup history`, apperror messages | Any operator reading output | Direct secret leak; the cheapest attack | `RedactedError` pattern (spec §6.2): categories only, never values. Passwords, key bytes, credentials, and repo URLs with embedded credentials never appear in user-facing text. History stores snapshot IDs and sizes only — IDs are not secret. |
| 6 | Subprocess environment (until the process adapter is retired) | Child process env block | Same-user processes reading `/proc/<pid>/environ` | Password + AWS keys visible to any same-user reader | Pass via `exec.Cmd.Env` only, NEVER argv (argv is visible in `ps`). This matches the existing adapter. The builtin engine has no subprocess at all. |
| 7 | Staged tmp files (`tmp/` inside the repo) | Repo directory, transient | Same as #4 | Partial ciphertext; with the master key an attacker could replay/observe in-flight data | tmp files are 0600 inside the 0700 repo, fsync'd then renamed; removed on error and on cancellation. No plaintext ever reaches tmp. |

## Cross-cutting rules

1. **Memory hygiene:** password bytes, KDF output, and the master key are
   zeroed (`crypto/subtle`-style clearing) when no longer needed. L1 accepts
   that Go cannot guarantee immediate reclamation — the rule is: never
   longer-lived than the repository object.
2. **No accidental copying:** never `fmt.Sprintf("%v", repo)` or JSON-marshal
   a struct containing `RepoConfig.Password`; the `RepoConfig` password field
   carries a `json:"-"`-equivalent discipline in tests and logs.
3. **Tests never leak:** test fixtures use placeholder env names
   (`EXAMPLE_...`), matching the repo's `scripts/check-docs.sh` contract.
4. **Doctor output:** doctor may print which env var NAME is configured,
   never its value or its presence/absence of a value beyond "set / not set".

## What this model deliberately does NOT cover (later phases)

- S3/R2 access keys at rest in the runtime storage file (existing contract,
  mode 0600; engine-L3 will inherit it).
- Remote attackers, network sniffing, and repository servers (no remote
  backend in L1).
- Locks as a mutual-exclusion / tampering vector (L4).
