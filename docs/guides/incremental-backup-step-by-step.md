# Step-by-Step Guide: Incremental Backup with Restic in Bqckup

This guide walks you through setting up, testing, and operating **Incremental Backups** in `bqckup` using the Restic engine.

---

## 1. Prerequisites

### A. Install Bqckup
```bash
# Build and install locally:
make build
sudo make install

# Verify bqckup installation:
bqckup version
```

### B. Install Restic Binary
Restic must be available in your system `$PATH`:
```bash
# Debian / Ubuntu
sudo apt-get update && sudo apt-get install -y restic

# RHEL / Rocky / Fedora
sudo dnf install -y restic

# macOS (Homebrew)
brew install restic

# Verify restic installation:
restic version
```

---

## 2. Directory Structure & Permissions Setup

Ensure data and configuration directories exist with strict permissions:

```bash
# Create application directories (0700)
sudo mkdir -p /etc/bqckup/config /etc/bqckup/sites
sudo mkdir -p /var/lib/bqckup/tmp /var/lib/bqckup/locks /var/backups/bqckup

# Set restrictive permissions
sudo chmod 700 /etc/bqckup /var/lib/bqckup /var/backups/bqckup
```

---

## 3. Step 1: Configure App Root (`/etc/bqckup/bqckup.yaml`)

Create `/etc/bqckup/bqckup.yaml`:

```yaml
app:
  state_database: /var/lib/bqckup/bqckup.db
  temporary_directory: /var/lib/bqckup/tmp
  lock_directory: /var/lib/bqckup/locks
  log_level: info
```

Set file permissions to `0600`:
```bash
sudo chmod 600 /etc/bqckup/bqckup.yaml
```

---

## 4. Step 2: Configure Storage Destination (`/etc/bqckup/config/storages.yaml`)

Configure your storage targets (Local filesystem, AWS S3, or Cloudflare R2):

```yaml
storages:
  # Option A: Local disk storage
  local-backup:
    type: local
    directory: /var/backups/bqckup
    primary: true

  # Option B: AWS S3 storage
  offsite-s3:
    type: s3
    bucket: my-backup-bucket
    access_key_id: EXAMPLE_ACCESS_KEY
    secret_access_key: EXAMPLE_SECRET_KEY
    region: us-east-1
    endpoint: https://s3.us-east-1.amazonaws.com # optional for standard AWS
    prefix: prod-backups                         # optional
    primary: false

  # Option C: Cloudflare R2 storage (S3-compatible)
  offsite-r2:
    type: r2
    bucket: my-r2-backup-bucket
    access_key_id: EXAMPLE_R2_ACCESS_KEY
    secret_access_key: EXAMPLE_R2_SECRET_KEY
    endpoint: https://<account_id>.r2.cloudflarestorage.com
    region: auto                                 # Cloudflare R2 requires 'auto'
    prefix: prod-backups                         # optional
    primary: false
```

> [!IMPORTANT]
> **Cloudflare R2 Requirements**:
> - Create an **R2 API Token** in Cloudflare Dashboard (*R2 → Manage R2 API Tokens*).
> - Set token permissions to **Object Read & Write** (not Read-Only).
> - Scope the token to **All buckets** or specifically to your target bucket.
> - Ensure `region` is set to `auto` and `endpoint` uses HTTPS.

Set file permissions to `0600`:
```bash
sudo chmod 600 /etc/bqckup/config/storages.yaml
```

---

## 5. Step 3: Configure Site (`/etc/bqckup/sites/web-app.yaml`)

Create `/etc/bqckup/sites/web-app.yaml`:

```yaml
site:
  name: web-app
  enabled: true
  
  # Enable Restic Incremental Backup ('incremental' or 'full')
  backup_mode: incremental
  incremental:
    # Reference to the environment variable holding the encryption password
    password_env: RESTIC_PASSWORD

  sources:
    files:
      include:
        - /var/www/web-app/uploads
        - /var/www/web-app/assets
      exclude:
        - "*.tmp"
        - "cache/**"
      follow_symlinks: false

    databases:
      - name: app-mysql
        enabled: true
        engine: mysql
        host: 127.0.0.1
        port: 3306
        database: production_app
        username: backup_user
        password: your-database-password

  destinations:
    - storage: offsite-s3   # Reference the storage name configured in storages.yaml

  policy:
    minimum_interval: 24h
    keep_last: 7
```

Pola `exclude` relatif dihitung dari setiap root `include`. `*.tmp` cocok
dengan nama file pada kedalaman apa pun, sedangkan akhiran `/**` seperti
`cache/**` mengecualikan direktori tersebut beserta seluruh isinya.

Set file permissions to `0600`:
```bash
sudo chmod 600 /etc/bqckup/sites/web-app.yaml
```

---

## 6. Step 4: Validate Configuration

Run `config validate` to check YAML syntax and schema adherence:

```bash
bqckup config validate
```

**Expected output:**
```text
valid schema v2 configuration: 1 site(s), 2 storage(s) in /etc/bqckup
```

---

## 7. Step 5: Export Encryption Password & Run Doctor Diagnostics

Export the environment variable defined in `password_env`:

```bash
# Export the repository encryption password
export RESTIC_PASSWORD="YourSuperSecurePassword123!"

# Run doctor preflight checks (use -E if running with sudo)
sudo -E bqckup doctor
```

**Expected output:**
```text
[✓] config: schema v2 valid (1 site(s), 2 storage(s))
[✓] temp_dir: /var/lib/bqckup/tmp is writable
[✓] lock_dir: /var/lib/bqckup/locks is writable
[✓] state_db_dir: /var/lib/bqckup is writable
[✓] engine:web-app: built-in incremental engine
[✓] secret:web-app:RESTIC_PASSWORD: environment variable is set
[✓] binary:mysqldump: found at /usr/bin/mysqldump
```

---

## 8. Step 6: Execute Baseline Backup

Run the backup for `web-app`:

```bash
sudo -E bqckup backup run web-app
```

**What happens behind the scenes:**
1. Restic initializes the repository at the destination (e.g. `s3:https://.../<bucket>/<prefix>/restic/web-app` or `/var/backups/bqckup/restic/web-app`) if not already initialized.
2. All files in `/var/www/web-app/uploads` and `/var/www/web-app/assets` are chunked, encrypted, and saved as a baseline snapshot.
3. Database `production_app` is exported using `mysqldump` to a compressed `.sql.gz` artifact and uploaded to the destination.
4. Snapshot ID and transferred byte stats are saved into the SQLite database (`/var/lib/bqckup/bqckup.db`).
5. Old backups are pruned to keep the last 7 snapshots.

**Output:**
```text
web-app: success (run run-1739856000000000000)
```

---

## 9. Step 7: Test Incremental Deduplication (Subsequent Runs)

Add or modify a single file to test incremental efficiency:

```bash
# Add a new file to the uploads folder
echo "New upload data" > /var/www/web-app/uploads/test.txt

# Run backup again (using --force to bypass the 24h minimum_interval)
sudo -E bqckup backup run web-app --force
```

**Result:**
Only the new `test.txt` block is transferred. All unchanged data is deduplicated instantly.

---

## 10. Step 8: View Backup History

Inspect recorded runs:

```bash
sudo bqckup history list --site web-app --limit 5
```

**Output:**
```text
ID                      STATUS    STARTED              DURATION    ARTIFACTS
run-1739856000000000000 success   2026-08-18 10:00:00  1.42s       2
run-1739856100000000000 success   2026-08-18 10:05:00  0.31s       2
```

---

## 11. Step 9: Verifying & Restoring Remote Snapshots (Direct Restic CLI)

Because Bqckup uses standard Restic repository format, you can query, verify, and restore directly using the `restic` CLI against your local or remote repository:

### A. For Local Storage
```bash
RESTIC_PASSWORD="YourSuperSecurePassword123!" restic -r /var/backups/bqckup/restic/web-app snapshots
```

### B. For Remote AWS S3 / Cloudflare R2 Storage
```bash
# 1. Export remote storage credentials and encryption password
export AWS_ACCESS_KEY_ID="<your-access-key>"
export AWS_SECRET_ACCESS_KEY="<your-secret-key>"
export AWS_DEFAULT_REGION="us-east-1"        # or 'auto' for Cloudflare R2
export RESTIC_PASSWORD="YourSuperSecurePassword123!"

# 2. Build the repository URL (matches storages.yaml destination):
# AWS S3:
REPO="s3:https://s3.us-east-1.amazonaws.com/my-backup-bucket/prod-backups/restic/web-app"
# Cloudflare R2:
# REPO="s3:https://<account_id>.r2.cloudflarestorage.com/my-r2-backup-bucket/prod-backups/restic/web-app"

# 3. List remote snapshots:
restic -r "$REPO" snapshots

# 4. Check repository integrity:
restic -r "$REPO" check

# 5. Restore snapshot to a target directory:
restic -r "$REPO" restore latest --target /tmp/restored-data
ls -la /tmp/restored-data
```

---

## 12. Remote Storage Layout (Full vs Incremental)

When inspecting your S3/R2 bucket directly:

- **Incremental Backups**:
  Stored at `<prefix>/restic/<site-name>/` with Restic repository structure:
  - `snapshots/` — JSON metadata for each snapshot run.
  - `data/` — Encrypted, deduplicated pack files.
  - `index/` — Index mapping blocks to packs.
  - `config` — Repository encryption key and format config.

- **Full Backups (`.tar.gz`)**:
  Stored at `<prefix>/bqckup/<site-name>/<timestamp>/`:
  - `files.tar.gz` — Compressed tarball of all included files.
  - `databases/<name>.sql.gz` — Compressed database dumps.

---

## 13. Troubleshooting Common Issues

| Issue / Error | Cause | Solution |
| :--- | :--- | :--- |
| `could not ensure incremental repository` / `client.BucketExists: Authorization` | R2/S3 credentials lack permission or are expired. | In Cloudflare R2, generate a new **R2 API Token** with *Object Read & Write* on the bucket. In AWS S3, verify IAM policy includes `s3:ListBucket`, `s3:GetObject`, and `s3:PutObject`. |
| `Stat: Authorization` on `restic snapshots` | Invalid credentials, expired token, or repository was never initialized. | Run `restic -r "$REPO" init` or run `bqckup backup run <site>` once to initialize. Check access keys. |
| Process hangs on `restic snapshots` (1–2 minutes) | Missing `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` in environment causing AWS SDK to query IMDS (`169.254.169.254`). | Export credentials in active terminal session: `export AWS_ACCESS_KEY_ID=...` and `export AWS_SECRET_ACCESS_KEY=...`. |
| `secret:site:RESTIC_PASSWORD: not set` with `sudo` | Linux `sudo` strips user environment variables by default (`env_reset`). | Run with `sudo -E bqckup doctor` or `sudo RESTIC_PASSWORD="..." bqckup backup run <site>`. |
| S3 upload fails on Cloudflare R2 | Missing `region: auto` or non-HTTPS endpoint. | Ensure `region: auto` and endpoint format `https://<account_id>.r2.cloudflarestorage.com` in `storages.yaml`. |
