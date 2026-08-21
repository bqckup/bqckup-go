# Laporan Inspeksi Bug — bqckup-go

- Tanggal inspeksi: 2026-08-21
- Branch: `7-incremental-backup` (working tree dengan WIP restic engine yang belum di-commit)
- Baseline commit: `7aeacf4`
- Metodologi: `go build`, `go vet`, `go test -race -count=1 ./...` (semua hijau), review statis
  pada seluruh `internal/`, gate kompatibilitas restic, dan repro tertulis untuk temuan serius.

---

## Ringkasan

Semua test suite lulus (`go test -race -count=1 ./...` hijau, 0 failure), tetapi review
menemukan **2 bug terkonfirmasi dengan repro** — satu di antaranya **data loss** — di
mesin restic engine yang baru (`internal/engine/restic`), plus beberapa temuan sekunder.

| # | Severity | Lokasi | Status |
|---|----------|--------|--------|
| 1 | **CRITICAL (data loss)** | `internal/engine/restic/repository/prune.go` | Terkonfirmasi repro |
| 2 | **HIGH** | `internal/engine/restic/lock/lock.go` + `facade/facade.go` | Terkonfirmasi repro |
| 3 | MEDIUM | `internal/backup/runner.go` (metadata artifact) | Terkonfirmasi live |
| 4 | MEDIUM | `internal/storage` + `runner.go` (timestamp key 1 detik) | Terkonfirmasi live |
| 5 | LOW | `internal/engine/restic/archiver/archiver.go` (hardlink) | Inspeksi |
| 6 | LOW | `internal/engine/restic/archiver/archiver.go` (basename duplikat) | Inspeksi |
| 7 | LOW | `internal/engine/restic/archiver/archiver.go` (error exclude ditelan) | Inspeksi |
| 8 | LOW | `internal/backup/runner.go` (FinishRun pakai ctx hidup) | Inspeksi |
| 9 | PROSES | Gate `restic_compat` selalu skip di environment ini | Terverifikasi |

---

## Bug 1 — CRITICAL: Prune menghapus data snapshot ber-tag lain (data loss)

**File:** `internal/engine/restic/repository/prune.go` — `ForgetAndPrune` / `sweep`

**Akar masalah:** `ForgetAndPrune` menandai blob "reachable" hanya dari snapshot yang
dipilih untuk dipertahankan (`kept` = snapshot dengan tag `site:<name>` yang terbaru).
Snapshot lain yang masih ada di repo (tag berbeda, tanpa tag, atau tag lama) **tidak
dihapus file-nya tetapi blob-nya tidak ditandai** → pack mereka dianggap mati dan
dihapus di `sweep`.

**Konsekuensi:**
- Snapshot file tetap ada tapi data-nya hilang → backup rusak secara diam-diam
  (`restic check` akan error, restore gagal).
- Kasus terparah: jika **tidak ada** snapshot yang cocok dengan tag (misal site di-rename
  di config, atau typo tag), `kept` kosong → `reachable` kosong → **seluruh pack di repo
  dihapus** pada run berikutnya. Seluruh riwayat backup lenyap dalam satu run.
- Trigger realistis: rename site di `configs/sites/*.yaml`, dua site berbagi satu repo
  (prefix sama), atau snapshot dibuat manual via restic CLI di repo yang sama.

**Repro (terkonfirmasi):** backup dir A dengan tag `site:a`, backup dir B dengan tag
`site:b`, lalu `ForgetAndPrune(keepLast=1, "site:a")`. Hasil:
```
site:b tree blob b66b8ba7... was pruned by ForgetAndPrune(site:a): data loss
```

**Fix yang disarankan:** tandai reachable dari SEMUA snapshot yang tidak di-forget
(bukan hanya `kept`), atau tolak prune jika ada snapshot di repo yang tidak membawa tag
site tersebut.

---

## Bug 2 — HIGH: Refresh lock yang gagal membocorkan lock eksklusif yang memblokir repo

**File:** `internal/engine/restic/lock/lock.go` — `Refresh`; `facade/facade.go` — `acquireExclusiveLock`

**Akar masalah:** `Refresh` membuat lock baru lalu menghapus lock lama dengan `ctx` yang
sama. Jika `Remove` gagal (ctx di-cancel, error network transient), lock lama tertinggal.
Goroutine refresh di facade berhenti pada error pertama, dan `release()` hanya menghapus
handle saat ini (`l.handle`) — file lock lama tidak pernah dihapus.

**Konsekuensi:**
- Lock bocor ber-timestamp segar (bukan stale) → backup berikutnya gagal dengan
  `ErrLocked` ("repository is already locked").
- `bqckup backup unlock` tidak bisa membersihkannya: `RemoveStale` hanya menghapus lock
  stale/invalid, bukan lock segar.
- Setelah 30 menit lock menjadi `ErrStaleExclusive` — yang juga **tidak** dihapus otomatis
  (deviasi desain L4) — baru bisa dibersihkan manual via `unlock`.
- Efek bersih: satu kegagalan transient selama backup panjang (mis. S3 timeout) membuat
  site tidak bisa backup minimal 30 menit dan menuntut intervensi manual.

**Repro (terkonfirmasi):** `lock.New(exclusive)` → `Refresh` dengan backend yang gagal
pada `Remove` → `Unlock` → `lock.New` kedua:
```
FAIL: leaked lock blocks the repository: repository is already locked exclusively by ... PID 192703
FAIL: 1 lock file(s) leaked after failed refresh
```

**Fix yang disarankan:** `Refresh` menghapus handle lama dengan `context.WithoutCancel`;
`release()` menghapus semua handle yang pernah dibuat proses ini (track list, bukan hanya
yang terakhir).

---

## Temuan sekunder (inspeksi)

### 3. MEDIUM — Metadata artifact incremental tidak akurat (terkonfirmasi live)
`internal/backup/runner.go` (blok `incremental`): `CreateArtifact` diisi
`ObjectKey: summary.SnapshotID`, `SHA256: summary.SnapshotID`, `Size: summary.DataAdded`.
- `SHA256` berisi snapshot ID, bukan hash objek yang disimpan — konsumen history yang
  memverifikasi SHA-256 akan gagal; kolom ini secara semantik salah.
- `Size` = `DataAdded` (byte baru hasil dedup), bukan ukuran data yang disimpan.

**Repro live** (3 run backup berturut-turut, file tidak berubah):
```
files|fa9d8227...|1365|fa9d8227...|stored   ← run 1 (DataAdded 1365)
files|617ea30d...|0   |617ea30d...|stored   ← run 2 (dedup penuh, Size=0)
files|a8931a27...|0   |a8931a27...|stored   ← run 3
```
Run 2/3 tercatat sebagai "0 byte, sha256 = id snapshot" padahal snapshot valid — history
terlihat seperti backup kosong.

### 4. MEDIUM — Forced backup (full mode) dalam detik yang sama GAGAL total (terkonfirmasi live)
`internal/storage/storage.go`: `TimestampLayout = "2006-01-02T15-04-05Z"` (resolusi 1 detik).
Object key artifact = `bqckup/<site>/<timestamp>/files.tar.gz`, dan store lokal maupun S3
menolak overwrite (`Lstat` exists / `IfNoneMatch: *`). Dua run dalam detik yang sama
(contoh: `backup run --force` dua kali berurutan, atau cron + manual bersamaan) → run kedua
**gagal total** dengan "could not store backup artifact".

**Repro live:**
```
arch: success (run eef7f21f-...)    ← 02:24:54Z
arch: skipped (minimum_interval)
arch: FAILED — could not store backup artifact   ← --force di 02:24:54Z yang sama
```
Run ketiga sukses hanya setelah detik berganti. Fix: tambahkan sufiks unik (run ID) pada
key saat terjadi tabrakan, atau perbesar resolusi timestamp.

### 5. LOW — Hard link tidak didedup
`archiver.nodeFor`/`saveFile` memperlakukan setiap hard link sebagai file terpisah;
konten disimpan berulang. Restic asli mendeteksi inode sama dan menyimpan konten sekali.
Hanya boros ruang, bukan bug korektness.

### 6. LOW — Dua include path dengan basename sama membuat backup gagal
`combineRoots` membuat node sintetis bernama `filepath.Base(path)`; dua path berbeda
dengan basename sama (mis. `/a/data` dan `/b/data`) memicu `tree.Add` → error
"nodes are not sorted by name" yang membingungkan. Tidak ada pesan error yang jelas.

### 7. LOW — Error pola exclude ditelan diam-diam
`archiver.excluded`: `if ok, _ := filepath.Match(...)` — error parsing pola diabaikan.
Pola invalid (mis. `[") tidak pernah cocok → file yang dimaksud user untuk di-exclude
ikut ter-backup tanpa peringatan. Restic asli gagal lantang pada pola invalid.

### 8. LOW — Jalur sukses `FinishRun` memakai ctx hidup
`runner.Run`: jalur gagal memakai `context.WithoutCancel(ctx)`, jalur sukses tidak.
Jika ctx di-cancel tepat setelah backup selesai, `FinishRun` gagal → baris history
tergantung di status `running` selamanya (run berikutnya tetap bisa jalan karena
`LastSuccessful` memfilter `status=success`, tapi history menampilkan run yang tidak
pernah selesai).

### 9. PROSES — Gate kompatibilitas restic tidak pernah jalan di environment ini
`internal/engine/restic/compat_test.go` di-tag `restic_compat` dan skip bila restic
< 0.17.0. Environment ini punya restic 0.16.4 (format v1):
```
--- SKIP: TestResticCheckSnapshotsRestore: restic 0.16.4 is repository format v1 only
--- SKIP: TestEngineOpensResticMadeRepo
--- SKIP: TestResticCheckAfterPrune
```
Klaim "verified against restic source" di docs hanya terverifikasi manual, tidak
terverifikasi kontinu di mesin ini. Jalankan gate sekali dengan restic >= 0.17.0
(`go test -tags=restic_compat ./internal/engine/restic/...`) sebelum mengandalkan
kompatibilitas format (pack/index/crypto/lock) terhadap binary asli.

---

## Catatan yang sudah dicek dan aman

- `go build`, `go vet`, `go test -race -count=1 ./...` — semua hijau.
- Crypto envelope (AES-256-CTR + Poly1305-AES), scrypt key file, urutan field index
  v2, pack header builder, tree sorting, lock format (0x02 || zstd || seal) — konsisten
  dengan dokumentasi verifikasi; tidak ditemukan bug.
- `repository.Init` idempotent; urutan tulis pack → index → snapshot sudah benar
  (crash-safe).
- `s3compat.Put` memakai `IfNoneMatch: *` (anti-overwrite); verifikasi size+SHA256
  setelah upload + cleanup saat gagal — benar.
- Aturan "retention tidak dijalankan setelah operasi gagal" dipatuhi di runner.
- Credential handling (mode 0600, non-symlink, env reference) — sesuai aturan repo.

---

## Lampiran: cara mereproduksi

Repro ditulis sebagai test sementara (`internal/engine/restic/prunecheck/`) dan dihapus
setelah konfirmasi agar `make verify` tetap hijau. Untuk menjalankan ulang:

1. **Bug 1:** buat repo, backup dua dir dengan tag berbeda, jalankan
   `ForgetAndPrune(1, "site:a")`, lalu cek `MasterIndex().Lookup(treeB)`.
2. **Bug 2:** `lock.New(exclusive)` → `Refresh` dengan backend yang gagal pada `Remove`
   pertama → `Unlock` → `lock.New` kedua harus gagal dengan `ErrLocked`.

## Cakupan pass kedua (semua dicek, tidak ditemukan bug tambahan)

- `files/archiver.go` (tar+gzip): deteksi symlink cycle via `EvalSymlinks` + map active,
  proteksi path traversal pada member tar, header dir trailing slash, exclude prefix — benar.
- `history` (GORM): `LastSuccessful` memfilter `status=success` ✓; ID uuid; migrasi AutoMigrate.
- `chunker/polynomials.go`: Ben-Or irreducibility test, DivMod/MulMod — sesuai upstream.
- `backend/layout.go`: pemetaan layout restic (data/<xx>, keys, index, snapshots, locks, tmp) ✓.
- `pack` parser sengaja test-only (komentar "move back when restore (L2)") — bukan bug.
- `s3compat/client.go`: signing SDK standar, retry 3x, region auto untuk R2 — benar.
- Mode bit di tree node (`0o755 | os.ModeDir`) konsisten dengan cara restic menyimpan mode
  (`uint32(fi.Mode())`) — bukan bug.
- Smoke test end-to-end (builtin engine): init → backup → dedup antar-run ✓, retention
  keep_last=2 menghapus snapshot tertua + pack mati ✓, snapshot files tersisa sesuai policy ✓.

## Status perbaikan

Belum ada perbaikan yang diterapkan — inspeksi saja. Bug 1 dan 2 sebaiknya diperbaiki
sebelum milestone L2/L4 dianggap selesai, dan masing-masing diberi regression test
yang gagal tanpa fix. Bug 4 (timestamp collision) mudah direpro dan berdampak langsung
pada penggunaan nyata (`--force` / cron).

Catatan: temuan #3 dan #4 dikonfirmasi lewat smoke test binary asli (`/tmp/bqckup`,
config di `/tmp/bt/conf`); artefak test ada di `/tmp/bt` bila ingin diperiksa.
