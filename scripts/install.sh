#!/bin/sh
set -eu

# Bqckup Installation & Bootstrap Script
# Supports both:
# 1. Standalone server installation via pre-built GitHub Release artifacts + SHA-256 verification
# 2. Local source installation via Makefile/Go incremental build cache

SKIP_BUILD=0
for arg in "$@"; do
    case "$arg" in
        --skip-build|-s)
            SKIP_BUILD=1
            ;;
        --help|-h)
            echo "Usage: $0 [--skip-build]"
            echo "Environment variables: GITHUB_REPO, BQCKUP_VERSION, BIN_DIR, CONFIG_DIR, DATA_DIR, BACKUP_DIR, LOG_DIR"
            exit 0
            ;;
    esac
done

# Configurable paths & repository options
GITHUB_REPO="${GITHUB_REPO:-bqckup/bqckup-go}"
BQCKUP_VERSION="${BQCKUP_VERSION:-latest}"
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/bqckup}"
DATA_DIR="${DATA_DIR:-/var/lib/bqckup}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/bqckup}"
TMP_DIR="${TMP_DIR:-${DATA_DIR}/tmp}"
LOCK_DIR="${LOCK_DIR:-${DATA_DIR}/locks}"
LOG_DIR="${LOG_DIR:-/var/log/bqckup}"

# Colors if interactive terminal
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    C_RESET='\033[0m'
    C_BOLD='\033[1m'
    C_GREEN='\033[32m'
    C_BLUE='\033[34m'
    C_YELLOW='\033[33m'
    C_RED='\033[31m'
else
    C_RESET=''
    C_BOLD=''
    C_GREEN=''
    C_BLUE=''
    C_YELLOW=''
    C_RED=''
fi

info() {
    printf "${C_BLUE}${C_BOLD}[INFO]${C_RESET} %s\n" "$1"
}

success() {
    printf "${C_GREEN}${C_BOLD}[SUCCESS]${C_RESET} %s\n" "$1"
}

warn() {
    printf "${C_YELLOW}${C_BOLD}[WARN]${C_RESET} %s\n" "$1"
}

error() {
    printf "${C_RED}${C_BOLD}[ERROR]${C_RESET} %s\n" "$1" >&2
}

# Check write permissions
can_write() {
    dir="$1"
    target="$dir"
    while [ ! -d "$target" ] && [ "$target" != "/" ]; do
        target="$(dirname "$target")"
    done
    [ -w "$target" ]
}

if [ "$(id -u)" -ne 0 ]; then
    if ! can_write "$BIN_DIR" || ! can_write "$CONFIG_DIR" || ! can_write "$DATA_DIR" || ! can_write "$BACKUP_DIR"; then
        warn "You are not running as root and might lack write permissions to system directories."
        warn "Run with sudo or provide writable custom directory paths, e.g.:"
        warn "  sudo ./scripts/install.sh"
    fi
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

info "Starting bqckup installation..."

# Cleanup trap for temporary downloads
TMP_WORK_DIR=""
cleanup() {
    if [ -n "$TMP_WORK_DIR" ] && [ -d "$TMP_WORK_DIR" ]; then
        rm -rf "$TMP_WORK_DIR"
    fi
}
trap cleanup EXIT INT TERM

# Detect OS and architecture
detect_platform() {
    OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
    ARCH="$(uname -m)"

    case "$ARCH" in
        x86_64|amd64)
            GOARCH="amd64"
            ;;
        aarch64|arm64)
            GOARCH="arm64"
            ;;
        *)
            error "Unsupported architecture: $ARCH (supported: amd64, arm64)"
            exit 1
            ;;
    esac

    if [ "$OS" != "linux" ]; then
        warn "bqckup is designed for Linux servers. Detected OS: $OS"
    fi
}

# 1. Acquire or build binary
if [ "$SKIP_BUILD" -eq 1 ] && [ -x "${BIN_DIR}/bqckup" ]; then
    info "Using existing binary at ${BIN_DIR}/bqckup (--skip-build)"
else
    BINARY_SOURCE=""

    # Strategy A: If running inside local git repository, build with Make / Go
    if [ -f "${REPO_ROOT}/go.mod" ] && [ -d "${REPO_ROOT}/cmd/bqckup" ]; then
        if command -v make >/dev/null 2>&1 && [ -f "${REPO_ROOT}/Makefile" ]; then
            info "Building bqckup from local source with make build..."
            make -C "${REPO_ROOT}" build
            BINARY_SOURCE="${REPO_ROOT}/bqckup"
        elif command -v go >/dev/null 2>&1; then
            info "Building bqckup from local source with go build..."
            (cd "${REPO_ROOT}" && go build -o bqckup ./cmd/bqckup)
            BINARY_SOURCE="${REPO_ROOT}/bqckup"
        fi
    fi

    # Strategy B: Download pre-built release artifact from GitHub Releases
    if [ -z "$BINARY_SOURCE" ] || [ ! -f "$BINARY_SOURCE" ]; then
        detect_platform
        info "Downloading pre-built bqckup binary for linux/${GOARCH} from GitHub Releases..."
        
        TMP_WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/bqckup-install.XXXXXX")"
        
        # Resolve release tag
        if [ "$BQCKUP_VERSION" = "latest" ]; then
            RELEASE_URL="https://github.com/${GITHUB_REPO}/releases/latest/download"
        else
            RELEASE_URL="https://github.com/${GITHUB_REPO}/releases/download/${BQCKUP_VERSION}"
        fi

        # Download archive and checksums if curl/wget is available
        ARCHIVE_FILE="${TMP_WORK_DIR}/bqckup.tar.gz"
        CHECKSUM_FILE="${TMP_WORK_DIR}/checksums.txt"

        download_file() {
            url="$1"
            dest="$2"
            if command -v curl >/dev/null 2>&1; then
                curl -sSfL "$url" -o "$dest"
            elif command -v wget >/dev/null 2>&1; then
                wget -q "$url" -O "$dest"
            else
                error "Neither curl nor wget is available for downloading releases."
                exit 1
            fi
        }

        # Attempt downloading release assets
        DOWNLOAD_SUCCESS=0
        if download_file "${RELEASE_URL}/checksums.txt" "${CHECKSUM_FILE}" 2>/dev/null; then
            # Find matching tarball name in checksums.txt
            ARCHIVE_NAME="$(grep -E "linux_${GOARCH}\.tar\.gz$" "${CHECKSUM_FILE}" | awk '{print $2}' | tr -d '\r' | head -n 1 || true)"
            if [ -n "$ARCHIVE_NAME" ] && download_file "${RELEASE_URL}/${ARCHIVE_NAME}" "${ARCHIVE_FILE}" 2>/dev/null; then
                info "Verifying SHA-256 checksum for ${ARCHIVE_NAME}..."
                EXPECTED_SHA="$(grep -E "[[:space:]]${ARCHIVE_NAME}$" "${CHECKSUM_FILE}" | awk '{print $1}')"
                ACTUAL_SHA="$(sha256sum "${ARCHIVE_FILE}" | awk '{print $1}')"
                if [ "$EXPECTED_SHA" = "$ACTUAL_SHA" ]; then
                    success "Checksum verified: ${ACTUAL_SHA}"
                    tar -xzf "${ARCHIVE_FILE}" -C "${TMP_WORK_DIR}"
                    if [ -f "${TMP_WORK_DIR}/bqckup" ]; then
                        BINARY_SOURCE="${TMP_WORK_DIR}/bqckup"
                        DOWNLOAD_SUCCESS=1
                    fi
                else
                    error "Checksum verification failed! Expected: $EXPECTED_SHA, Got: $ACTUAL_SHA"
                    exit 1
                fi
            fi
        fi

        if [ "$DOWNLOAD_SUCCESS" -ne 1 ]; then
            if command -v bqckup >/dev/null 2>&1; then
                BINARY_SOURCE="$(command -v bqckup)"
                info "Using existing bqckup found in PATH: ${BINARY_SOURCE}"
            else
                error "Could not build from source or download release from https://github.com/${GITHUB_REPO}."
                error "If this is a private repository, please clone the repo and run 'make setup'."
                exit 1
            fi
        fi
    fi

    # Install binary
    if [ "${BINARY_SOURCE}" != "${BIN_DIR}/bqckup" ]; then
        info "Installing binary to ${BIN_DIR}/bqckup..."
        mkdir -p "${BIN_DIR}"
        cp "${BINARY_SOURCE}" "${BIN_DIR}/bqckup"
        chmod 0755 "${BIN_DIR}/bqckup"
        success "Binary installed to ${BIN_DIR}/bqckup"
    fi
fi

# 2. Create system directory tree
info "Creating system directories..."
mkdir -p "${CONFIG_DIR}/config" "${CONFIG_DIR}/sites" "${DATA_DIR}/tmp" "${DATA_DIR}/locks" "${BACKUP_DIR}" "${LOG_DIR}"
chmod 0700 "${CONFIG_DIR}" "${CONFIG_DIR}/config" "${CONFIG_DIR}/sites" \
           "${DATA_DIR}" "${DATA_DIR}/tmp" "${DATA_DIR}/locks" "${BACKUP_DIR}" 2>/dev/null || true
chmod 0750 "${LOG_DIR}" 2>/dev/null || true
success "Directory tree created and secured (mode 0700)."

# 3. Populate default configuration templates
info "Configuring default templates in ${CONFIG_DIR}..."

# App configuration: bqckup.yaml
if [ ! -f "${CONFIG_DIR}/bqckup.yaml" ]; then
    cat <<EOF > "${CONFIG_DIR}/bqckup.yaml"
app:
  state_database: ${DATA_DIR}/bqckup.db
  temporary_directory: ${TMP_DIR}
  lock_directory: ${LOCK_DIR}
  log_level: info
  log_file: ${LOG_DIR}/bqckup.log
EOF
    chmod 0600 "${CONFIG_DIR}/bqckup.yaml"
    info "Created ${CONFIG_DIR}/bqckup.yaml"
fi

# Storage configuration: config/storages.yaml
if [ ! -f "${CONFIG_DIR}/config/storages.yaml" ]; then
    cat <<EOF > "${CONFIG_DIR}/config/storages.yaml"
storages:
  local-primary:
    type: local
    directory: ${BACKUP_DIR}
    primary: true
EOF
    chmod 0600 "${CONFIG_DIR}/config/storages.yaml"
    info "Created ${CONFIG_DIR}/config/storages.yaml"
fi

# Storage examples: config/storages.example.yaml
if [ ! -f "${CONFIG_DIR}/config/storages.example.yaml" ]; then
    cat <<'EOF' > "${CONFIG_DIR}/config/storages.example.yaml"
storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true

  # AWS S3 / MinIO example
  # idrive-s3:
  #   type: s3
  #   bucket: your-backup-bucket
  #   access_key_id: EXAMPLE_ACCESS_KEY
  #   secret_access_key: EXAMPLE_SECRET_KEY
  #   region: us-east-1
  #   endpoint: https://s3.us-east-1.amazonaws.com
  #   prefix: backups
  #   primary: false

  # Cloudflare R2 example
  # cloudflare-r2:
  #   type: r2
  #   bucket: your-r2-bucket
  #   access_key_id: EXAMPLE_R2_ACCESS_KEY
  #   secret_access_key: EXAMPLE_R2_SECRET_KEY
  #   endpoint: https://<account_id>.r2.cloudflarestorage.com
  #   prefix: backups
  #   primary: false
EOF
    chmod 0600 "${CONFIG_DIR}/config/storages.example.yaml"
fi

# Default site template: sites/example.yaml
if [ ! -f "${CONFIG_DIR}/sites/example.yaml" ]; then
    cat <<EOF > "${CONFIG_DIR}/sites/example.yaml"
site:
  name: example
  enabled: false
  sources:
    files:
      include:
        - /srv/example/data
      exclude:
        - /srv/example/data/cache
      follow_symlinks: false
    databases: []
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 24h
    keep_last: 7
EOF
    chmod 0600 "${CONFIG_DIR}/sites/example.yaml"
    info "Created template site at ${CONFIG_DIR}/sites/example.yaml"
fi

# 4. Check external database tools (informational, non-blocking)
if command -v mysqldump >/dev/null 2>&1; then
    success "mysqldump found: $(command -v mysqldump)"
else
    warn "mysqldump not found in PATH (install mariadb-client / mysql-client if MySQL backups are needed)"
fi

if command -v pg_dump >/dev/null 2>&1; then
    success "pg_dump found: $(command -v pg_dump)"
else
    warn "pg_dump not found in PATH (install postgresql-client if PostgreSQL backups are needed)"
fi

# 5. Validate configuration with bqckup CLI
info "Validating configuration..."
if "${BIN_DIR}/bqckup" --config-dir "${CONFIG_DIR}" config validate; then
    success "Configuration validated successfully!"
else
    error "Configuration validation failed. Please check files in ${CONFIG_DIR}."
    exit 1
fi

# 6. Summary
printf "\n"
printf "${C_GREEN}${C_BOLD}======================================================${C_RESET}\n"
printf "${C_GREEN}${C_BOLD}  bqckup installed and configured successfully!       ${C_RESET}\n"
printf "${C_GREEN}${C_BOLD}======================================================${C_RESET}\n"
printf "\n"
printf "${C_BOLD}Installed Locations:${C_RESET}\n"
printf "  Binary:          %s/bqckup\n" "${BIN_DIR}"
printf "  Configuration:   %s\n" "${CONFIG_DIR}"
printf "  State & Locks:   %s\n" "${DATA_DIR}"
printf "  Default Backups: %s\n" "${BACKUP_DIR}"
printf "\n"
printf "${C_BOLD}Quick Start:${C_RESET}\n"
printf "  1. Edit site config in %s/sites/<site>.yaml (set enabled: true)\n" "${CONFIG_DIR}"
printf "  2. Validate configuration:  %s/bqckup config validate\n" "${BIN_DIR}"
printf "  3. List configured sites:    %s/bqckup backup list\n" "${BIN_DIR}"
printf "  4. Run backup:               %s/bqckup backup run <site>\n" "${BIN_DIR}"
printf "  5. Set up cron:              0 2 * * * %s/bqckup backup run <site>\n" "${BIN_DIR}"
printf "\n"
