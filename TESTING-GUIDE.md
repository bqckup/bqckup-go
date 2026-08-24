# Panduan Pengujian Fitur Bqckup (schema-v2)

Panduan praktis untuk menguji setiap fitur bqckup satu per satu, memakai sandbox lokal tanpa menyentuh infrastruktur produksi. Semua contoh diasumsikan dijalankan dari root repository (`~/Documents/bqckup-go`) dengan binary `./bqckup` hasil `go build -o bqckup ./cmd/bqckup`.

> Dokumentasi ini panduan pengujian manual. Untuk kebijakan pengujian otomatis (unit/integrasi), lihat `docs/testing.md`.

---

## 0. Setup Awal Sandbox

Struktur config directory bqckup:

```text
sandbox/
├── docker-compose.yml        # MySQL dummy (untuk fitur backup database)
├── db/seed.sql               # data seed
├── scratch/                  # direktori kecil untuk backup files
└── config/                   # --config-dir
    ├── bqckup.yaml
    ├── config/storages.yaml
    └── sites/<site>.yaml
```

### 0.1 Buat direktori

```bash
mkdir -p sandbox/config/config sandbox/config/sites sandbox/db sandbox/scratch
echo "hello from files backup" > sandbox/scratch/readme.txt
mkdir -p sandbox/backups
```

### 0.2 `sandbox/config/bqckup.yaml`

```yaml
app:
  state_database: data/bqckup.db
  temporary_directory: tmp
  lock_directory: locks
  log_level: info
```

Path relatif di-resolve terhadap config dir (`sandbox/config/`).

### 0.3 `sandbox/config/config/storages.yaml`

```yaml
storages:
  local-primary:
    type: local
    directory: /home/aexion_linggar/Documents/bqckup-go/sandbox/backups
    primary: true
```

`directory` WAJIB absolute path — validator menolak path relatif.

### 0.4 `sandbox/config/sites/mysql.yaml`

```yaml
site:
  name: mysql
  enabled: true
  sources:
    files:
      include:
        - /home/aexion_linggar/Documents/bqckup-go/sandbox/scratch
    databases:
      - name: mysql-testdb
        enabled: true
        engine: mysql
        host: 127.0.0.1
        port: 3306
        database: testdb
        username: backup_user
        password: dummysecret
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1m
    keep_last: 3
```

```bash
chmod 600 sandbox/config/sites/mysql.yaml
```

### 0.5 `sandbox/docker-compose.yml`

```yaml
services:
  mysql:
    image: mysql:8
    container_name: bqckup-test-mysql
    environment:
      MYSQL_ROOT_PASSWORD: dummyroot
      MYSQL_DATABASE: testdb
      MYSQL_USER: backup_user
      MYSQL_PASSWORD: dummysecret
    ports:
      - "3306:3306"
    volumes:
      - ./db/seed.sql:/docker-entrypoint-initdb.d/seed.sql:ro
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "localhost", "-p$$MYSQL_ROOT_PASSWORD"]
      interval: 2s
      timeout: 5s
      retries: 30
      start_period: 10s
```

### 0.6 `sandbox/db/seed.sql`

```sql
CREATE DATABASE IF NOT EXISTS testdb;
USE testdb;

CREATE TABLE items (
  id INT PRIMARY KEY AUTO_INCREMENT,
  name VARCHAR(100) NOT NULL,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO items (name) VALUES ('alpha'), ('beta'), ('gamma');

CREATE TABLE items_audit (
  id INT PRIMARY KEY AUTO_INCREMENT,
  item_id INT,
  action VARCHAR(20),
  happened_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE PROCEDURE count_items()
  SELECT COUNT(*) AS total FROM items;

CREATE TRIGGER items_after_insert
  AFTER INSERT ON items
  FOR EACH ROW
  INSERT INTO items_audit (item_id, action) VALUES (NEW.id, 'insert');

CREATE VIEW item_names AS SELECT id, name FROM items;
```

Catatan: seed hanya jalan saat **boot pertama volume kosong**. Untuk seed ulang: `docker compose down -v`.

### 0.7 Siapkan MySQL dummy

```bash
docker compose -f sandbox/docker-compose.yml up -d --wait
docker compose -f sandbox/docker-compose.yml ps   # harus (healthy)
```

### 0.8 Perbaiki privilege `backup_user` (WAJIB)

User buatan `MYSQL_USER` hanya dapat `ALL ON testdb.*` — tidak cukup untuk `mysqldump --routines --triggers` (perlu membaca definisi procedure) dan dump tablespace (perlu `PROCESS`). Tanpa grant ini, export gagal dengan "could not export database".

```bash
docker compose -f sandbox/docker-compose.yml exec mysql \
  mysql -uroot -pdummyroot -e "GRANT ALL PRIVILEGES ON *.* TO 'backup_user'@'%' WITH GRANT OPTION; FLUSH PRIVILEGES;"
```

(Untuk container dummy ini wajar. Di produksi, gunakan grant minimal: `SELECT, SHOW VIEW, PROCESS, LOCK TABLES, RELOAD, REPLICATION CLIENT` + privilege routine yang relevan.)

---

## 1. Backup Files

Fitur: arsip `files.tar.gz` dari path `sources.files.include` (tar + gzip), dengan pola exclude dan opsi symlink.

### Uji 1.1 — Include dasar

```bash
mkdir -p sandbox/scratch/sub
echo "data" > sandbox/scratch/sub/data.txt
./bqckup --config-dir sandbox/config backup run mysql --force
```

Verifikasi isi arsip:

```bash
ART=$(ls -d sandbox/backups/bqckup/mysql/*/ | tail -1)
tar -tzf "$ART/files.tar.gz"
# harus memuat scratch/readme.txt dan scratch/sub/data.txt
```

### Uji 1.2 — Exclude

Tambahkan ke site config:

```yaml
    files:
      include:
        - /home/aexion_linggar/Documents/bqckup-go/sandbox/scratch
      exclude:
        - /home/aexion_linggar/Documents/bqckup-go/sandbox/scratch/sub
```

Run ulang, verifikasi `sub/` tidak ada di dalam tar.

### Uji 1.3 — Follow symlinks

```bash
ln -s /tmp sandbox/scratch/link
```

Default `follow_symlinks: false` — link diarsipkan sebagai link (atau diabaikan), bukan isinya. Set `follow_symlinks: true`, run ulang, bandingkan isi tar.

### Uji 1.4 — Validasi (negatif)

- `include` kosong pada enabled site: `sources.files.include: at least one path is required`
- Path relatif: `must be an absolute path`

---

## 2. Backup Database

Fitur: dump MySQL (`mysqldump`) / PostgreSQL (`pg_dump`) di-stream langsung ke gzip, password via environment variable, verifikasi SHA-256, tercatat di history.

### Uji 2.1 — Run sukses

```bash
./bqckup --config-dir sandbox/config backup run mysql --force
```

### Uji 2.2 — Verifikasi isi dump

```bash
ART=$(ls -d sandbox/backups/bqckup/mysql/*/ | tail -1)
gunzip -c "$ART/databases/mysql-testdb.sql.gz" | grep -E "CREATE PROCEDURE|CREATE TRIGGER|alpha|CREATE VIEW"
```

Harus muncul: `count_items`, `items_after_insert`, baris seed `alpha`, dan view. Ini bukti flag `--routines --triggers` benar-benar membawa schema.

### Uji 2.3 — Preflight tool hilang

Rename `mysqldump` sementara tidak mungkin tanpa sudo; sebagai gantinya jalankan dengan PATH kosong tidak praktis. Simulasi CLI: batasi PATH lalu jalankan command yang membuka aplikasi (bukan `config validate`, yang hanya memuat config tanpa preflight):

```bash
PATH=/usr/local/bin:/usr/local/sbin ./bqckup --config-dir sandbox/config backup list
# → required database exporter is unavailable (exit 3)
```

Alternatif: unit test sudah menutup ini — test preflight ada di `internal/app` (`go test -race ./internal/app/ -run Preflight -v`), test exporter di `internal/backup/database` (`go test -race ./internal/backup/database/`). Error yang muncul bila tool tidak ada: `required database exporter is unavailable`.

### Uji 2.4 — Kredensial salah (uji redaksi)

Ubah `password: dummysecret` menjadi `password: salah`, run ulang. Hasil yang diharapkan: **hanya** `could not export database` — tanpa detail kredensial/host (error asli mysqldump sengaja disembunyikan). Bandingkan dengan dump manual:

```bash
MYSQL_PWD=salah mysqldump --host=127.0.0.1 --port=3306 --user=backup_user \
  --single-transaction --quick --routines --triggers testdb
# error asli terlihat di sini: Access denied ...
```

### Uji 2.5 — PostgreSQL

Ganti `engine: mysql` menjadi `engine: postgres` dengan container postgres. Argumen berbeda: `--format=plain --no-owner --no-privileges`, env `PGPASSWORD`. Preflight butuh `pg_dump` (`postgresql-client`).

### Gotcha penting

| Gotcha | Penjelasan |
|---|---|
| `host: localhost` vs `127.0.0.1` | `localhost` = Unix socket (di dalam container, tidak ada di host). DB di Docker: selalu `127.0.0.1` atau nama host TCP. |
| Privilege `MYSQL_USER` | Tanpa grant tambahan, `--routines` gagal (SHOW CREATE PROCEDURE). Lihat langkah 0.8. |
| Seed hanya sekali | `docker compose down -v` untuk reset data. |

---

## 3. Local Storage

Fitur: penyimpanan backup di direktori lokal, layout key `bqckup/<site>/<UTC-timestamp>/...`, write-once (tidak pernah menimpa), mode aman.

### Uji 3.1 — Layout & mode

```bash
ls -la sandbox/backups/bqckup/mysql/
# satu direktori per run, format timestamp: 2026-08-12T03-44-09.123456789Z (UTC)
stat -c "%a %n" sandbox/backups/bqckup/mysql/*/
# direktori set: 700
find sandbox/backups/bqckup/mysql/*/ -type f -exec stat -c "%a %n" {} \;
# files.tar.gz dan *.sql.gz: 600
```

### Uji 3.2 — Write-once

Penyimpanan lokal memakai staging file (`.bqckup-staging-*`) lalu `RENAME_NOREPLACE` — kernel menolak rename jika tujuan sudah ada. Bukti perilaku: dua run pada detik yang sama praktis mustahil; mekanismenya terverifikasi lewat unit test (`go test -race ./internal/storage/local/`). Artinya bqckup **tidak akan pernah menimpa** backup set yang sudah ada.

### Uji 3.3 — Validasi path

- `directory` relatif: `must be an absolute path`
- Key traversal (`..`, absolute, backslash) ditolak oleh `ValidateKey` — tidak mungkin object tersimpan di luar root.

---

## 4. S3 & R2 Storage

Fitur: penyimpanan di object storage S3-compatible (AWS S3, Cloudflare R2, MinIO, dsb). Upload dengan `IfNoneMatch: *` (tolak jika object sudah ada), verifikasi pasca-upload via `HeadObject` (size + metadata `bqckup-sha256`/`bqckup-size`), object gagal verifikasi langsung dihapus.

### Uji 4.1 — MinIO lokal (pengganti AWS tanpa kredensial cloud)

Client bqckup mengaktifkan path-style addressing untuk custom endpoint — kompatibel MinIO.

```bash
docker run -d --name bqckup-test-minio -p 9000:9000 -p 9001:9001 \
  -e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin \
  minio/minio server /data --console-address ":9001"
```

Buat bucket `bqckup-test` lewat web console `http://127.0.0.1:9001` (login minioadmin/minioadmin), atau dengan `mc`:

```bash
docker run --rm --add-host=host.docker.internal:host-gateway --entrypoint sh minio/mc -c \
  "mc alias set local http://host.docker.internal:9000 minioadmin minioadmin && mc mb local/bqckup-test"
```

> **Catatan Linux:** `host.docker.internal` hanya otomatis tersedia di Docker Desktop (macOS/Windows). Di Docker Engine Linux native hostname itu tidak ada — flag `--add-host=host.docker.internal:host-gateway` di atas memetakannya ke IP host (Docker ≥ 20.10). Alternatif: `--network=host` + URL `http://127.0.0.1:9000`.

Tambah storage di `sandbox/config/config/storages.yaml`:

```yaml
  minio-primary:
    type: s3
    bucket: bqckup-test
    endpoint: http://127.0.0.1:9000
    region: us-east-1
    access_key_id: minioadmin
    secret_access_key: minioadmin
    prefix: dev
```

Ubah site `destinations`:

```yaml
  destinations:
    - storage: local-primary
    - storage: minio-primary
```

Run backup, lalu verifikasi object:

```bash
docker run --rm --add-host=host.docker.internal:host-gateway --entrypoint sh minio/mc -c \
  "mc alias set local http://host.docker.internal:9000 minioadmin minioadmin && mc ls -r local/bqckup-test"
# harus ada dev/bqckup/mysql/<timestamp>/files.tar.gz dan databases/mysql-testdb.sql.gz
docker run --rm --add-host=host.docker.internal:host-gateway --entrypoint sh minio/mc -c \
  "mc alias set local http://host.docker.internal:9000 minioadmin minioadmin && mc stat local/bqckup-test/dev/bqckup/mysql/<timestamp>/databases/mysql-testdb.sql.gz"
# metadata: bqckup-sha256 dan bqckup-size
```

### Uji 4.2 — R2

Provider `r2`: `region` wajib `auto`, endpoint wajib HTTPS. Tanpa akun R2, verifikasi terbatas pada: `config validate` menerima `type: r2` dengan `endpoint: https://...` dan `region: auto` (region lain ditolak: `region must be auto for r2 storage`). Uji integrasi opsional (membutuhkan akun sungguhan):

```bash
BQCKUP_S3_INTEGRATION_CONFIG=/private/config \
BQCKUP_S3_INTEGRATION_STORAGE=testing \
go test -tags=integration ./internal/storage/s3compat -run TestDisposableS3CompatibleStorage -count=1 -v
```

### Uji 4.3 — Kredensial S3 salah

Ganti `secret_access_key` dengan nilai salah, lalu jalankan backup:

```bash
sed -i 's|secret_access_key: minioadmin|secret_access_key: salah|' sandbox/config/config/storages.yaml
./bqckup --config-dir sandbox/config backup run mysql --force
```

Hasil: `could not store backup artifact`, exit code **4** (kategori storage), dan set lokal menjadi parsial (`files.tar.gz` saja — run berhenti di store pertama, export DB tidak sampai jalan).

> **Mengapa bukan `S3-compatible upload failed`?** Pesan itu adalah cause tersembunyi (`hiddenError`) satu level di bawah. CLI mencetak `UserMessage` = pesan `apperror` **terluar** — di sini `could not store backup artifact`. Pesan dalam tidak pernah tampil; diagnosis wajib lewat reproduksi manual.

Kontras: jalankan `mc` dengan kredensial salah — `mc` justru menampilkan error asli provider (mis. `AccessDenied`). Itu bukti redaksi bqckup bekerja.

Kembalikan kredensial dan pastikan mode file (file berisi kredensial wajib `0600`):

```bash
sed -i 's|secret_access_key: salah|secret_access_key: minioadmin|' sandbox/config/config/storages.yaml
chmod 600 sandbox/config/config/storages.yaml
```

---

## 5. History SQLite 3

Fitur: setiap run, artifact, status, ukuran, dan SHA-256 dicatat di database SQLite (`state_database`, default `data/bqckup.db`). Mode WAL, `foreign_keys=on`, `busy_timeout=5000`, file `0600`.

### Uji 5.1 — Lokasi & mode

```bash
ls -la sandbox/config/data/
# bqckup.db (600), bqckup.db-wal, bqckup.db-shm (sidecar WAL saat berjalan)
```

### Uji 5.2 — Inspeksi langsung

```bash
sqlite3 sandbox/config/data/bqckup.db '.tables'
sqlite3 sandbox/config/data/bqckup.db 'select id, site_name, status, started_at from backup_runs order by started_at desc limit 5;'
sqlite3 sandbox/config/data/bqckup.db 'select source_kind, source_name, status, size, sha256 from artifacts order by id desc limit 5;'
```

### Uji 5.3 — CLI

```bash
./bqckup --config-dir sandbox/config history list
./bqckup --config-dir sandbox/config history list --site mysql --limit 3
./bqckup --config-dir sandbox/config --output json history list
```

### Uji 5.4 — Sifat penting

- Run yang di-skip (`minimum_interval` / `already_running`) **tidak** membuat record — skip terjadi sebelum `CreateRun`.
- Run gagal tetap tercatat (status `failed` + alasan) — history adalah audit log append-only; `keep_last` TIDAK menghapus history, hanya storage.

---

## 6. Retention Policy

Fitur: mempertahankan N backup set terbaru per site per destinasi. Set = semua artifact satu run di bawah `bqckup/<site>/<timestamp>/`. `keep_last` menghitung **set (run)**, bukan file. Retention murni dari listing storage — tidak membaca history.

### Uji 6.1 — keep_last memangkas set terlama

```bash
for i in 1 2 3 4 5; do
  ./bqckup --config-dir sandbox/config backup run mysql --force
done
ls sandbox/backups/bqckup/mysql/
# hanya 3 direktori timestamp (keep_last: 3), yang tertua terhapus
```

Dengan banyak destination, setiap destinasi dipangkas independen ke `keep_last` (local 3 set, MinIO 3 set).

### Uji 6.2 — minimum_interval melewatkan run

Ubah `minimum_interval: 1m` menjadi `minimum_interval: 24h` (default), lalu:

```bash
./bqckup --config-dir sandbox/config backup run mysql
# run sukses
./bqckup --config-dir sandbox/config backup run mysql
# output: skipped, reason minimum_interval — dan TIDAK ada record baru di history
./bqckup --config-dir sandbox/config backup run mysql --force
# --force melewati throttle, run jalan
```

Kembalikan ke `1m` untuk demo berikutnya.

### Uji 6.3 — Gagal prune = run ditandai failed

Skenario nyata yang pernah terjadi: set dir milik root (akibat `sudo ./bqckup`) tidak bisa dihapus oleh user biasa.

```bash
sudo ./bqckup --config-dir sandbox/config backup run mysql --force
./bqckup --config-dir sandbox/config backup run mysql --force
# backup sukses disimpan, tapi output: backup completed but retention could not be applied (exit 4)
# history mencatat run sebagai failed
```

Pulihkan:

```bash
sudo chown -R $USER:$USER sandbox/backups
```

Pelajaran: jangan pernah campur sudo dan non-sudo pada storage yang sama; kepemilikan harus konsisten.

### Uji 6.4 — Set parsial ikut dihitung

Run yang menyimpan `files.tar.gz` lalu gagal di database tetap meninggalkan set (tanpa `.sql.gz`). Set parsial ini ikut dihitung dan ikut terprune oleh run sukses berikutnya. Bukti: bikin export gagal (password salah), lihat set parsial muncul, lalu perbaiki dan run beberapa kali — set parsial akhirnya terhapus.

---

## 7. Schema-v2 Config & Validasi

Fitur: format config terstruktur (root + storages + site), versi schema eksplisit (otomatis v2), dan validasi menyeluruh saat load.

### Uji 7.1 — Perintah dasar

```bash
./bqckup --config-dir sandbox/config config validate
./bqckup --config-dir sandbox/config backup list
./bqckup --config-dir sandbox/config version
```

### Uji 7.2 — Matriks validasi negatif

Ubah config, jalankan `config validate`, pastikan pesan error sesuai, lalu kembalikan:

| Perubahan | Pesan error yang diharapkan |
|---|---|
| `version: 1` di root | `must equal 2` |
| `engine: oracle` | `must be mysql or postgres` |
| `port: 0` | `must be between 1 and 65535` |
| `directory:` relatif di storage | `must be an absolute path` |
| hapus `files.include` dari enabled site | `at least one path is required` |
| dua storage `primary: true` | `at most one storage may be primary` |
| nama file site != `site.name` | `must match filename` |
| dua site dengan nama sama | `duplicate site name` |
| site file berpassword mode `0644` | `password-bearing site file must have mode 0600` |
| site file berpassword symlink | `password-bearing site file must not be a symlink` |

### Uji 7.3 — `init`

```bash
./bqckup --config-dir /tmp/bqckup-init-test init
# membuat kerangka config dengan contoh disabled — aman untuk belajar
```

---

## 8. Multi-Site Support

Fitur: banyak site dalam satu config dir; lock per-site memungkinkan site berbeda berjalan paralel, site sama diserialkan.

### Uji 8.1 — Tambah site kedua

`sandbox/config/sites/web.yaml` (files-only):

```yaml
site:
  name: web
  enabled: true
  sources:
    files:
      include:
        - /home/aexion_linggar/Documents/bqckup-go/sandbox/scratch
  destinations:
    - storage: local-primary
  policy:
    minimum_interval: 1m
    keep_last: 2
```

```bash
./bqckup --config-dir sandbox/config backup list     # mysql + web
./bqckup --config-dir sandbox/config backup run web --force
ls sandbox/backups/bqckup/                           # mysql/ dan web/ terpisah
```

### Uji 8.2 — Site berbeda paralel, site sama serial

Terminal 1 dan terminal 2, mulai hampir bersamaan:

```bash
# Terminal 1
./bqckup --config-dir sandbox/config backup run mysql --force
# Terminal 2
./bqckup --config-dir sandbox/config backup run web --force
# keduanya sukses — lock beda file (mysql.lock vs web.lock)
```

Site sama dua proses:

```bash
./bqckup --config-dir sandbox/config backup run mysql --force &
./bqckup --config-dir sandbox/config backup run mysql --force
# salah satu: skipped, reason already_running — tanpa error, tanpa record history
```

Jika run pertama terlalu cepat selesai hingga race tidak terjadi, perkecil peluang dengan data besar atau ulangi beberapa kali.

---

## 9. Primary Storage Concept

Fitur: `primary: true` pada satu storage = destinasi default untuk enabled site yang **tidak mendeklarasikan** `destinations`. Injeksi terjadi di memori saat load — file YAML tidak berubah.

### Uji 9.1 — Defaulting bekerja

Buat site tanpa `destinations` (`sandbox/config/sites/db.yaml` menyalin mysql.yaml lalu hapus blok `destinations`), lalu:

```bash
./bqckup --config-dir sandbox/config config validate   # lulus
./bqckup --config-dir sandbox/config backup run db --force
ls sandbox/backups/bqckup/db/                           # tersimpan di local-primary
```

### Uji 9.2 — Dua primary ditolak

Tambah `primary: true` ke storage kedua: `at most one storage may be primary`.

### Uji 9.3 — Destinations eksplisit mengalahkan primary

Site `mysql` punya `destinations: [local-primary, minio-primary]` — ubah `primary: true` pindah ke `minio-primary`, run `mysql`: hasil tetap ke kedua destination sesuai daftar eksplisit; primary tidak berpengaruh.

### Uji 9.4 — Nol primary = site tanpa destinations ditolak

Hapus semua `primary: true`. Site tanpa `destinations` kini **ditolak saat validasi** dengan `at least one destination is required` (injeksi primary hanya terjadi jika ada storage ber-`primary: true`).

---

## 10. Lock File (flock)

Fitur: `flock(2)` advisory lock per site, non-blocking. File `<lock_directory>/<site>.lock` mode `0600`. Kernel melepas lock otomatis saat proses mati — tidak ada stale lock.

### Uji 10.1 — Lock aktif

```bash
./bqckup --config-dir sandbox/config backup run mysql --force &
sleep 0.2
./bqckup --config-dir sandbox/config backup run mysql --force
# proses kedua: skipped (already_running)
ls -la sandbox/config/locks/
# mysql.lock 600
```

### Uji 10.2 — Crash recovery (kill -9)

```bash
./bqckup --config-dir sandbox/config backup run mysql --force &
PID=$!
kill -9 $PID
./bqckup --config-dir sandbox/config backup run mysql --force
# langsung sukses — lock dilepas kernel, tanpa cleanup manual
```

---

## 11. Credential Hygiene

Fitur: 4 lapis perlindungan kredensial — (1) mode file config `0600` di-enforce saat load, (2) password tidak pernah di argv, (3) redaksi error dari proses dump/S3, (4) semua file/direktori data owner-only.

### Uji 11.1 — Mode 0600 di-enforce

```bash
chmod 644 sandbox/config/sites/mysql.yaml
./bqckup --config-dir sandbox/config config validate
# password-bearing site file must have mode 0600
chmod 600 sandbox/config/sites/mysql.yaml
```

### Uji 11.2 — Symlink ditolak

```bash
mv sandbox/config/sites/mysql.yaml /tmp/mysql.yaml
ln -s /tmp/mysql.yaml sandbox/config/sites/mysql.yaml
./bqckup --config-dir sandbox/config config validate
# password-bearing site file must not be a symlink
mv -f /tmp/mysql.yaml sandbox/config/sites/mysql.yaml && chmod 600 sandbox/config/sites/mysql.yaml
```

### Uji 11.3 — Password tidak tampil di proses

Saat dump berjalan (buat DB besar atau tambahkan `sleep` via shim), cek:

```bash
ps aux | grep mysqldump
# args hanya --host=... --user=... — TANPA password (password via MYSQL_PWD env)
```

### Uji 11.4 — Redaksi error

Sudah diuji di 2.4 (kredensial DB) dan 4.3 (kredensial S3): output bqckup hanya pesan generik, tidak pernah memuat secret, host, atau response provider.

### Uji 11.5 — Mode file hasil

```bash
find sandbox/backups -type f -exec stat -c "%a %n" {} \;
# semua 600
find sandbox/backups -mindepth 1 -type d -exec stat -c "%a %n" {} \;
# semua 700
```

### Uji 11.6 — Unit test verifikasi

```bash
go test -race ./internal/backup/database/ -run 'Redaction|Password' -v
# membuktikan: password tidak di argv, env key benar, error redaksi tidak bocorkan secret
```

---

## 12. Gotcha Ringkasan

| Gotcha | Gejala | Solusi |
|---|---|---|
| `host: localhost` dengan DB Docker | `could not export database` (socket tidak ketemu) | `host: 127.0.0.1` |
| User `MYSQL_USER` tanpa grant | `SHOW CREATE PROCEDURE` gagal | grant privilege (lihat 0.8) |
| `sudo` bqckup bercampur user biasa | retention gagal, set dir root-owned | `sudo chown -R $USER:$USER sandbox/backups` |
| `minimum_interval` default 24h | run kedua di-skip tanpa jelas | set `1m` untuk demo, atau `--force` |
| Site file tidak `0600` | validasi gagal | `chmod 600` |
| Seed MySQL tidak jalan lagi | data tidak muncul | `docker compose down -v` lalu `up -d` |
| `files.include` wajib meski site database-only | validasi gagal | beri path scratch absolute |

---

## 13. Teardown

```bash
docker compose -f sandbox/docker-compose.yml down -v
docker rm -f bqckup-test-minio 2>/dev/null || true
sudo rm -rf sandbox/backups    # jika masih ada file root-owned
rm -rf sandbox /tmp/bqckup-init-test
```

Perintah `make verify` dan `sh scripts/check-docs.sh` memastikan perubahan kode dan dokumentasi tetap sesuai standar repo.
