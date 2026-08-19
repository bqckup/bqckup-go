# M10: Packaging, Release Automation & Unified Installer

## Problem Statement
How Might We create a reproducible, multi-architecture GitHub Actions release workflow that builds verified Linux binaries (`amd64` & `arm64`) with injected metadata and SHA-256 manifests, while empowering [`scripts/install.sh`](../../scripts/install.sh) and [`Makefile`](../../Makefile) to seamlessly support both standalone one-line server installations (`curl ... | sudo bash`) and fast local development builds?

---

## Recommended Direction: Unified Release Matrix & Smart Installer

Combine a lightweight GitHub Actions workflow matrix with Makefile-backed build targets and a dual-mode installer script:

1. **GitHub Actions Release Workflow (`.github/workflows/release.yml`)**:
   - **Trigger**: Git tags (`v*`) and manual `workflow_dispatch`.
   - **Matrix**: `linux/amd64` (native runner) and `linux/arm64` (via `gcc-aarch64-linux-gnu` cross-compiler with `CGO_ENABLED=1` for SQLite).
   - **Ldflags Injection**: Injects version tag, commit hash, and build timestamp into `github.com/bqckup/bqckup-go/internal/buildinfo`.
   - **Artifact Packaging**: Bundles each binary into `bqckup_<version>_linux_<arch>.tar.gz`.
   - **Checksums**: Generates a standard `checksums.txt` (SHA-256) for integrity verification.
   - **Release Publication**: Publishes a GitHub Release with attached tarballs and manifest via `softprops/action-gh-release@v2`.

2. **Unified Smart Installer (`scripts/install.sh`)**:
   - **Mode 1 (Repository Clone)**: Detects local repository source and compiles instantly with `make build` (using Go's incremental cache).
   - **Mode 2 (Standalone Server / Curl)**: Automatically detects host architecture (`x86_64` vs `aarch64/arm64`), fetches the latest matching release archive and checksum from GitHub Releases, verifies SHA-256, and installs to `/usr/local/bin/bqckup`.
   - **Directory & Configuration Provisioning**: Creates `/etc/bqckup`, `/var/lib/bqckup`, and `/var/backups/bqckup` with mode `0700`, provisions working default templates with mode `0600`, and executes `bqckup config validate`.

3. **Streamlined `Makefile`**:
   - Provides clean developer commands: `make build`, `make install`, `make setup`, and `make verify`.

---

## Key Assumptions to Validate

- [ ] **Cross-compilation with CGO**: `CGO_ENABLED=1 CC=aarch64-linux-gnu-gcc GOARCH=arm64` compiles SQLite clean without glibc version mismatches across standard Linux distributions.
- [ ] **Ldflags wiring**: `internal/buildinfo` accurately reflects the git tag (`vX.Y.Z`) and commit in `bqckup version`.
- [ ] **Release download fallback**: While the repository remains private, `scripts/install.sh` gracefully detects the private environment and uses `make build` / local source.

---

## MVP Scope

### In Scope
- `.github/workflows/release.yml`: Release workflow with build matrix for `linux/amd64` and `linux/arm64`.
- Version metadata injection via `-ldflags`.
- Generation and publication of `checksums.txt` (SHA-256).
- Dual-mode `scripts/install.sh` supporting local Make build and remote GitHub Release binary download + verification.
- Documentation updates in `README.md` and `USER-GUIDE.md`.

### Out of Scope (Not Doing & Why)
- **Homebrew / APT / RPM repository hosting**: Adds repository signing key custody complexity; out of scope for M10.
- **macOS / Windows binary targets**: Bqckup is specifically a Linux server backup tool using Linux filesystem and CLI database process tools.
- **GoReleaser binary dependency**: Standard Make + GitHub Actions steps keep the repository clean without third-party tool lock-in.

---

## Open Questions & Review
- Should `scripts/install.sh` default to a configurable repository URL (e.g. `GITHUB_REPO="bqckup/bqckup-go"`) with support for environment overrides (`BQCKUP_VERSION=...`)? *(Recommended: Yes)*
