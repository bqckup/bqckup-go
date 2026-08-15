#!/bin/sh
set -eu

# Bqckup Installation & Bootstrap Script
# Fast installation: leverages Makefile/Go cache, creates directories,
# populates default templates with 0600 permissions, and validates setup.

SKIP_BUILD=0
for arg in "$@"; do
    case "$arg" in
        --skip-build|-s)
            SKIP_BUILD=1
            ;;
        --help|-h)
            echo "Usage: $0 [--skip-build]"
            echo "Environment variables: BIN_DIR, CONFIG_DIR, DATA_DIR, BACKUP_DIR"
            exit 0
            ;;
    esac
done

# Configurable paths
BIN_DIR="${BIN_DIR:-/usr/local/bin}"
CONFIG_DIR="${CONFIG_DIR:-/etc/bqckup}"
DATA_DIR="${DATA_DIR:-/var/lib/bqckup}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/bqckup}"
TMP_DIR="${TMP_DIR:-${DATA_DIR}/tmp}"
LOCK_DIR="${LOCK_DIR:-${DATA_DIR}/locks}"

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

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

info "Starting bqckup installation..."

# 1. Build or locate binary
if [ "$SKIP_BUILD" -eq 1 ] && [ -x "${BIN_DIR}/bqckup" ]; then
    info "Using existing binary at ${BIN_DIR}/bqckup (--skip-build)"
else
    BINARY_SOURCE=""
    if [ -f "${REPO_ROOT}/go.mod" ]; then
        if command -v make >/dev/null 2>&1 && [ -f "${REPO_ROOT}/Makefile" ]; then
            info "Building bqckup via make build..."
            make -C "${REPO_ROOT}" build
            BINARY_SOURCE="${REPO_ROOT}/bqckup"
        elif command -v go >/dev/null 2>&1; then
            info "Building bqckup via go build..."
            (cd "${REPO_ROOT}" && go build -o bqckup ./cmd/bqckup)
            BINARY_SOURCE="${REPO_ROOT}/bqckup"
        fi
    fi

    if [ -z "$BINARY_SOURCE" ] || [ ! -f "$BINARY_SOURCE" ]; then
        if command -v bqckup >/dev/null 2>&1; then
            BINARY_SOURCE="$(command -v bqckup)"
            info "Found bqckup in PATH: ${BINARY_SOURCE}"
        else
            error "Could not build or locate bqckup binary. Ensure Go or Make is installed."
            exit 1
        fi
    fi

    # Install binary if not already at destination
    if [ "${BINARY_SOURCE}" != "${BIN_DIR}/bqckup" ]; then
        info "Installing binary to ${BIN_DIR}/bqckup..."
        mkdir -p "${BIN_DIR}"
        cp "${BINARY_SOURCE}" "${BIN_DIR}/bqckup"
        chmod 0755 "${BIN_DIR}/bqckup"
        success "Binary installed to ${BIN_DIR}/bqckup"
    fi
fi

# 2. Create directory tree in one pass
info "Creating system directories..."
mkdir -p "${CONFIG_DIR}/config" "${CONFIG_DIR}/sites" "${DATA_DIR}/tmp" "${DATA_DIR}/locks" "${BACKUP_DIR}"
chmod 0700 "${CONFIG_DIR}" "${CONFIG_DIR}/config" "${CONFIG_DIR}/sites" \
           "${DATA_DIR}" "${DATA_DIR}/tmp" "${DATA_DIR}/locks" "${BACKUP_DIR}" 2>/dev/null || true
success "Directory tree created and secured (mode 0700)."

# 3. Populate default configuration templates (fast write)
info "Configuring default templates in ${CONFIG_DIR}..."

# App configuration: bqckup.yaml
if [ ! -f "${CONFIG_DIR}/bqckup.yaml" ]; then
    cat <<EOF > "${CONFIG_DIR}/bqckup.yaml"
app:
  state_database: ${DATA_DIR}/bqckup.db
  temporary_directory: ${TMP_DIR}
  lock_directory: ${LOCK_DIR}
  log_level: info
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
