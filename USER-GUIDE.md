# Panduan Pengguna bqckup

Panduan singkat untuk **mengoperasikan** bqckup: menyiapkan config, menjalankan backup, dan mengecek hasilnya. Bahasa sederhana, langsung praktik.

> Untuk pengujian fitur langkah demi langkah, lihat `TESTING-GUIDE.md`. Untuk referensi config lengkap, lihat `docs/configuration-v2.md`.

---

## 1. Apa yang dilakukan bqckup

bqckup menjalankan **backup** untuk:

- **File/direktori** → dikompres jadi `files.tar.gz` (mode `full`); pada
  `backup_mode: incremental` file masuk ke repositori format restic
  (deduplikasi + enkripsi), bukan arsip tar.gz
- **Database MySQL** (via `mysqldump`) dan **PostgreSQL** (via `pg_dump`) → dikompres jadi `databases/<nama>.sql.gz` (semua mode)

Hasilnya disimpan ke satu atau lebih **storage**:

- **local** — direktori di disk
- **s3** — AWS S3 atau S3-compatible lain (mis. MinIO)
- **r2** — Cloudflare R2

Setiap run dicatat di **history** (database SQLite). Backup lama dihapus otomatis sesuai **retention** (`keep_last`).

---

## 2. Persiapan & Instalasi

### Cara Cepat (Make Setup / Install Script)

Jalankan perintah berikut untuk otomatis compile/unduh binary ke `/usr/local/bin`, membuat direktori data/config (`0700`), dan menyiapkan template config dengan permission `0600`:

```bash
# Dari repositori lokal:
sudo make setup
# atau: sudo ./scripts/install.sh

# Dari server standalone (unduh binary release pre-built + verifikasi SHA-256):
curl -fsSL https://raw.githubusercontent.com/bqckup/bqckup-go/main/scripts/install.sh | sudo bash
```

### Cara Manual

1. **Build / install binary:**

   ```bash
   make build
   sudo make install
   ```

2. **Alat database** (hanya jika ada backup database):

   - MySQL/MariaDB → `mysqldump` (`apt install mariadb-client` atau `mysql-client`)
   - PostgreSQL → `pg_dump` (`apt install postgresql-client`)

   Tanpa alat ini, semua command backup akan gagal di awal dengan exit code 3 (preflight).

3. **Config dir:** bqckup membaca config dari `/etc/bqckup` secara default. Bisa diganti dengan flag `--config-dir <direktori>` di setiap command.

---

## 3. Setup pertama (Manual)

```bash
sudo mkdir -p /etc/bqckup
sudo bqckup --config-dir /etc/bqckup init
```

`init` membuat kerangka config:

```text
/etc/bqckup/bqckup.yaml            ← pengaturan app (path data)
/etc/bqckup/config/storages.yaml   ← daftar storage tujuan
/etc/bqckup/sites/<site>.yaml      ← satu file per site backup
```

Isi default: satu storage `local-primary` + satu site contoh (`enabled: false`). `init` menolak menimpa file yang sudah ada.

Langkah selanjutnya: edit ketiga file sesuai kebutuhan, lalu validasi:

```bash
bqckup --config-dir /etc/bqckup config validate
```

Output sukses: `valid schema v2 configuration: N site(s), N storage(s) in /etc/bqckup`.

> **Catatan:** `config validate` hanya mengecek struktur config. Ketersediaan `mysqldump`/`pg_dump` baru dicek saat command backup/history dijalankan.

---

## 4. Isi config

### 4.1 `bqckup.yaml` (root)

```yaml
version: 2

app:
  state_database: /var/lib/bqckup/bqckup.db
  temporary_directory: /var/lib/bqckup/tmp
  lock_directory: /var/lib/bqckup/locks
  log_level: info
```

- `state_database` → file history SQLite. Path relatif dihitung dari config dir.
- Path lain boleh relatif juga. Pastikan direktori induknya bisa dibuat/ditulis oleh user yang menjalankan bqckup.

### 4.2 `config/storages.yaml` (tujuan penyimpanan)

```yaml
storages:
  local-primary:
    type: local
    directory: /var/backups/bqckup
    primary: true

  cloudflare-r2:
    type: r2
    bucket: nama-bucket
    endpoint: https://<account_id>.r2.cloudflarestorage.com
    region: auto
    access_key_id: <r2-access-key>
    secret_access_key: <r2-secret>
    prefix: dev          # opsional
```

Aturan:

- `directory` (local) wajib **absolute**.
- `primary: true` maksimal **satu** storage. Site tanpa `destinations` otomatis memakai primary.
- `r2`: `region` wajib **`auto`**, endpoint wajib **HTTPS** (`https://<account_id>.r2.cloudflarestorage.com`).
- `s3` custom endpoint (mis. MinIO): boleh `http://127.0.0.1:9000`, region bebas (mis. `us-east-1`).
- File yang berisi `access_key_id`/`secret_access_key` **wajib mode `0600`**.

### 4.3 `sites/<site>.yaml` (apa yang dibackup)

Nama file harus sama dengan `site.name` (mis. `sites/web.yaml` → `name: web`).

```yaml
version: 2

site:
  name: web
  enabled: true
  sources:
    files:
      include:
        - /srv/web/uploads
      exclude:
        - "*.tmp"
        - "cache/**"
      follow_symlinks: false
    databases:
      - name: web-mysql
        enabled: true
        engine: mysql
        host: 127.0.0.1
        port: 3306
        database: aplikasi
        username: backup_user
        password: <password>
      - name: web-postgres
        enabled: true
        engine: postgres
        host: 127.0.0.1
        port: 5432
        database: lainnyadb
        username: backup_user
        password: <password>
  destinations:
    - storage: local-primary
    - storage: cloudflare-r2
  policy:
    minimum_interval: 24h
    keep_last: 7
```

Penjelasan:

| Bagian | Arti |
| --- | --- |
| `backup_mode` | Mode backup: `full` (default, arsip `.tar.gz`) atau `incremental` (snapshot restic). |
| `incremental.password_env` | Nama environment variable password enkripsi repositori restic (wajib jika `backup_mode: incremental`). |
| `sources.files.include` | Daftar path file/dir yang dibackup. Wajib **absolute**, minimal 1 jika site `enabled`. |
| `sources.files.exclude` | Path absolute atau pola glob relatif terhadap setiap `include`. `*.tmp` berlaku pada semua kedalaman; `cache/**` melewati direktori secara rekursif. Semantiknya sama untuk mode full dan incremental. |
| `sources.files.follow_symlinks` | Ikuti symlink saat membungkus arsip (default `false`). |
| `sources.databases` | Daftar database. Boleh kosong (`[]`) jika hanya backup file. |
| `destinations` | Storage tujuan. Kosong = ikut `primary`. |
| `policy.minimum_interval` | Jarak minimum antar run (default `24h`). Run terlalu cepat di-skip. Bypass dengan `--force`. |
| `policy.keep_last` | Jumlah **set backup** terbaru yang dipertahankan **per site per storage** (default 7). |

Tips database & incremental:

- **Incremental:** Cocok untuk direktori file besar (mis. WordPress uploads). Mesin pure-Go bawaan menyimpan hanya blok data yang berubah (deduplikasi forever-incremental) ke storage lokal atau S3/R2. Tidak perlu binary Restic. Password enkripsi diambil dari environment variable yang direferensikan pada `password_env` (mis. `export RESTIC_PASSWORD="..."`). Field lama `incremental.engine` harus dihapus dari config.
- **MySQL:** gunakan `127.0.0.1`, bukan `localhost` — `localhost` memakai Unix socket dan bisa gagal untuk MySQL di Docker. bqckup otomatis pakai `--single-transaction --quick --routines --triggers`. Untuk MySQL 8, user backup perlu grant rutin (lihat §8).
- **PostgreSQL:** `--username` di config **wajib** (default pg_dump adalah user OS). bqckup otomatis pakai `--no-owner --no-privileges`.
- Password database **tidak pernah** muncul di argumen proses — exporter memakai env `MYSQL_PWD` / `PGPASSWORD`. Password repository incremental dibaca langsung dari environment variable yang ditunjuk `password_env` dan diteruskan ke engine hanya di memori.
- File site yang berisi `password:` **wajib mode `0600`**, regular file, bukan symlink.

---

## 5. Operasi sehari-hari

Semua command memakai config dir yang sama:

```bash
# cek kesehatan sistem, permission, dan binary exporter database (mysqldump, pg_dump)
bqckup doctor

# lihat semua site, status, dan last successful
bqckup backup list

# jalankan backup untuk satu site (pakai --force bila ingin abaikan minimum_interval)
bqckup backup run web
bqckup backup run web --force

# hapus lock repository yang stale (bila backup sebelumnya crash dan
# lock exclusive lama memblokir run berikutnya)
bqckup backup unlock web

# lihat history run terakhir (default 20 baris)
bqckup history list
bqckup history list --site web --limit 10
```

Output `backup run` sukses:

```text
web: success (run <run-id>)
```

Kalau `minimum_interval` belum terpenuhi, run di-skip (bukan error):

```text
web: skipped (minimum interval not reached)
```

Semua command mendukung output JSON: tambahkan `--output json` (mis. `bqckup backup list --output json`).

---

## 6. Di mana hasil backup berada

```text
local : <directory>/bqckup/<site>/<timestamp>/files.tar.gz
s3/r2 : <prefix/>bqckup/<site>/<timestamp>/files.tar.gz
        <prefix/>bqckup/<site>/<timestamp>/databases/<nama>.sql.gz
```

Contoh: `/var/backups/bqckup/web/2026-08-13T06-18-03.123456789Z/files.tar.gz`

- `<timestamp>` = waktu run dalam UTC, format **dengan tanda hubung** (`2026-08-13T06-18-03.123456789Z`). Di output `history list` formatnya pakai titik dua — jangan disalin silang.
- Setiap objek di storage bersifat **write-once**: bqckup menolak menimpa objek yang sudah ada.
- Satu run = satu set. Kalau run gagal di tengah, set **parsial** bisa tertinggal (normal, aman).

---

## 7. Jadwal otomatis

bqckup tidak punya scheduler sendiri — gunakan cron:

```cron
# jalankan site "web" tiap hari jam 02:30
30 2 * * * /usr/local/bin/bqckup backup run web

# site lain tiap 6 jam
0 */6 * * * /usr/local/bin/bqckup backup run blog
```

Catatan untuk cron:

- Tanpa `--force`, run yang terlalu dekat dengan run sebelumnya otomatis di-skip — aman dipakai dengan cron yang rapat.
- Kalau bqckup dijalankan sebagai root (mis. via `/etc/cron.d`), pastikan **kepemilikan file data konsisten** — lihat §8.
- Dua run bersamaan untuk site yang sama aman: yang kedua otomatis di-skip (lock).

---

## 8. Masalah umum & artinya

| Gejala / pesan error | Arti | Solusi |
| --- | --- | --- |
| `config validation error ...` (exit 2) | Config salah struktur (path tidak absolute, nama site tidak cocok dengan nama file, region r2 bukan `auto`, dll.) | Baca pesannya — bqckup menyebut field yang salah. |
| `required database exporter is unavailable` (exit 3) | `mysqldump`/`pg_dump` tidak ada di PATH | Install client DB, atau nonaktifkan database di site. |
| `could not export database` (exit 4) | Export database gagal. Detail tidak ditampilkan (redaksi — disengaja) | Cek: container/service DB hidup? (`docker ps` / `systemctl status mysql`), host `127.0.0.1` bukan `localhost`, user/password benar, user punya akses. Test manual: `MYSQL_PWD=... mysqldump -h 127.0.0.1 ...` |
| `could not store backup artifact` (exit 4) | Upload ke storage gagal (mis. kredensial S3/R2 salah, endpoint salah, network down) | Test storage manual (mis. `mc ls` untuk S3/R2), cek kredensial & mode file 0600. |
| `backup completed but retention could not be applied` (exit 4) | Run sukses tapi hapus backup lama gagal | Biasanya masalah permission/kepemilikan file backup. |
| `already_running` (skip) | Ada run lain untuk site yang sama masih jalan | Tunggu atau cek proses. |
| Error `must have mode 0600` / `must not be a symlink` | File config berisi kredensial tapi mode/hak salah | `chmod 600 <file>`; kalau dibuat lewat editor/redirect, cek lagi mode-nya. |
| Run dengan `sudo` lalu run biasa gagal di retention | File backup jadi milik root | Jangan campur `sudo` dan non-sudo. Pilih satu user untuk semua operasi. |

**Exit codes:** `2` = config salah · `3` = preflight gagal · `4` = execution/storage/cancellation gagal · `1` = error lain · `0` = sukses.

### MySQL 8: grant untuk backup_user

bqckup selalu dump routines/triggers. Untuk MySQL 8, buat user backup dengan:

```sql
CREATE USER 'backup_user'@'%' IDENTIFIED BY '<password>';
GRANT SELECT, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, PROCESS ON *.* TO 'backup_user'@'%';
GRANT ALL PRIVILEGES ON *.* TO 'backup_user'@'%' WITH GRANT OPTION;  -- diperlukan agar dump routines jalan
```

(Lihat dokumentasi MySQL-mu untuk grant yang lebih ketat; baris `WITH GRANT OPTION` ini dummy dan hanya diperlukan agar `mysqldump --routines` tidak error di MySQL 8.)

---

## 9. Restore (manual)

bqckup belum punya command restore. Restore dilakukan manual:

1. Ambil artifact dari storage (local: copy file; S3/R2: `mc cp`).
2. Ekstrak:

   ```bash
   tar -xzf files.tar.gz
   gunzip -c databases/web-mysql.sql.gz > web-mysql.sql
   ```

3. Import database:

   ```bash
   # MySQL
   MYSQL_PWD=<password> mysql -h 127.0.0.1 -u backup_user aplikasi < web-mysql.sql
   # PostgreSQL
   PGPASSWORD=<password> psql -h 127.0.0.1 -U backup_user -d lainnyadb -f web-postgres.sql
   ```

---

## 10. Aturan keamanan (ringkas)

- File config berisi `password:` / `access_key_id` / `secret_access_key` → **wajib** regular file, mode `0600`, bukan symlink. bqckup menolak config yang melanggar.
- Password DB lewat env (`MYSQL_PWD`/`PGPASSWORD`), tidak pernah di argv.
- Pesan error sengaja di-redaksi (tidak membocorkan respons provider) — diagnosis lewat reproduksi manual.
- Jangan commit config berisi kredensial asli ke git.
