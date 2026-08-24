# Mesin Restic Bawaan, Dijelaskan Tanpa Istilah Teknis

Panduan ini menjelaskan, dengan bahasa sehari-hari, apa yang sebenarnya
dilakukan oleh mesin Restic yang ditulis ulang di dalam proyek ini dan
kenapa ia dibangun. Dokumen ini menerjemahkan spesifikasi desain, dokumen
perencanaan, dan milestone backlog ke dalam bahasa yang mudah dipahami.
Untuk aturan teknis yang persis, lihat dokumen-dokumen yang tercantum di
bagian akhir.

---

## 1. Masalah yang diselesaikannya

Bqckup membuat cadangan (backup). Sebelum pekerjaan ini, mode backup
"incremental"-nya bekerja dengan memanggil program eksternal bernama
`restic` yang harus dipasang terpisah di setiap mesin. Artinya:

- pengguna harus memasang dan merawat satu alat tambahan,
- setiap backup memulai proses eksternal baru (lambat, berantakan),
- satu program Go yang kecil tidak lagi "satu program Go yang kecil".

Tujuan pekerjaan ini: **membangun ulang bagian-bagian penting dari Restic di
dalam bqckup itu sendiri**, sehingga satu file bqckup saja sudah bisa
melakukan backup yang cepat, hemat ruang, dan terenkripsi tanpa alat
eksternal apa pun.

Tantangannya: hasilnya harus berbicara "bahasa" yang sama dengan Restic
asli. Seseorang harus bisa membuka backup buatan bqckup dengan Restic asli,
dan sebaliknya. Janji inilah yang mendorong hampir semua keputusan teknis.

## 2. Gambaran besar: backup adalah sebuah brankas

Bayangkan repositori backup sebagai sebuah **brankas** di sebuah gudang. Di
dalamnya:

| Konsep | Arti sederhana |
| --- | --- |
| Repository | Brankasnya: satu folder berisi banyak file kecil. |
| Snapshot | Catatan bertanggal yang berkata "beginilah wujud semuanya pada hari ini". Satu per proses backup. |
| Tree | Peta sebuah folder: file apa saja di dalamnya, nama, ukuran, izin akses, dan di mana isinya disimpan. |
| Blob | Satu potong konten yang tersimpan: bisa potongan isi file, atau peta folder. |
| Pack | Peti pengiriman yang memuat banyak blob sekaligus, supaya kita memindahkan file yang lebih sedikit tapi lebih besar. |
| Index | Katalog gudang: "potongan X ada di peti Y". Kita bisa cek apa yang sudah dimiliki tanpa membuka peti. |
| Key | Kartu akses brankas yang terenkripsi, yang dilindungi oleh kata sandi Anda. |

Setiap proses: bqckup berjalan menyusuri folder Anda, membangun peta-peta,
memotong isi file menjadi potongan-potongan, menyimpan potongan yang belum
pernah dilihat, memperbarui katalog, dan terakhir menulis snapshot.
Snapshot ditulis **paling akhir**, sehingga backup yang terputus di tengah
jalan tidak akan pernah meninggalkan brankas yang rusak — snapshot
sebelumnya tetap sah.

## 3. Kenapa backup kedua hampir gratis (deduplikasi)

Fitur andalannya: **cadangkan data yang sama dua kali, dan hampir tidak ada
yang baru tersimpan pada kali kedua.**

Dua trik yang membuat ini bekerja:

1. **Pemotongan berdasarkan isi.** File tidak dipotong menjadi blok
   berukuran tetap. Sebuah pemindai berjalan membaca byte demi byte dan
   menentukan titik potong dari isinya sendiri. Hasilnya: jika Anda
   menyunting file dengan menyisipkan satu paragraf di tengah, hanya
   potongan yang terkena suntingan yang berubah — semua potongan lain tetap
   persis sama. (Inilah ide "Rabin". Resep pemotongan yang persis adalah
   sebuah "polynomial" acak yang dipilih saat brankas dibuat dan disimpan di
   konfigurasinya, sehingga setiap proses berikutnya memotong dengan cara
   yang sama.)

2. **Sidik jari + pengecekan katalog.** Setiap potongan mendapat sidik jari
   unik (angka panjang yang dihitung dari byte-nya — byte sama, angka sama).
   Sebelum menyimpan sebuah potongan, bqckup bertanya ke katalog: "apakah
   saya sudah punya sidik jari ini?" Jika ya, potongan itu tidak disimpan
   lagi sama sekali. Pengecekan inilah sebabnya backup kedua hanya menulis
   sedikit pembukuan, bukan data Anda.

Ini juga berarti file yang sama persis di folder berbeda, atau di banyak
mesin yang mencadangkan ke brankas yang sama, hanya disimpan satu kali.

## 4. Privasi dan perlindungan dari perusakan

Semua yang disimpan terenkripsi:

- **Gemboknya:** kata sandi Anda melewati resep penguatan kunci yang lambat
  (scrypt) dan membuka kunci asli brankas. Desain dua lapis ini berarti
  kunci asli tidak pernah menyentuh disk tanpa enkripsi, dan menebak kata
  sandi menjadi mahal bagi penyerang.
- **Brankasnya:** setiap potongan diacak dengan AES-256, keluarga enkripsi
  yang sama dengan yang dipakai perbankan dan pemerintahan.
- **Segel anti-rusak:** setiap potongan juga mendapat segel semacam sidik
  jari (Poly1305) yang diperiksa saat dibaca. Jika seseorang membalik satu
  bit saja, pembacaan gagal dengan jelas, bukan diam-diam mengembalikan data
  yang salah.
- **Plastik penyusut:** potongan dimampatkan (zstd) sebelum disegel,
  sehingga brankas lebih kecil tanpa kehilangan apa pun.

Rahasia juga dilindungi dari kebocoran tak sengaja: kata sandi dan kunci
tidak pernah muncul di log, pesan kesalahan, baris perintah, atau basis
data riwayat. Pesan kesalahan tentang keduanya sengaja dibuat samar.

## 5. Aturan rumah yang membuatnya bisa dipercaya

Beberapa keputusan desain yang tenang melakukan banyak pekerjaan keamanan:

- **Tulis semuanya lewat area pementasan.** File ditulis ke tempat
  sementara, dibuang penuh ke disk, lalu diganti nama ke posisi akhirnya
  dalam satu langkah atomik. Kerusakan di tengah penulisan tidak akan pernah
  meninggalkan file setengah jadi di dalam brankas.
- **Konsistensi katalog.** Katalog (index) yang menunjuk ke peti-peti hanya
  ditulis setelah peti-peti itu tersimpan dengan aman. Katalog tidak akan
  pernah menunjuk ke peti yang tidak ada.
- **Izin akses ketat.** File dan folder brankas dibuat dengan izin paling
  ketat yang masuk akal (0600 / 0700).
- **Pembatalan itu aman.** Ctrl-C menghentikan setiap proses baca/tulis,
  membersihkan file pementasan, dan — karena snapshot ditulis paling akhir
  — meninggalkan kumpulan backup sebelumnya utuh.
- **Tidak ada pembiaran diam-diam.** Jika pekerjaan yang diminta tidak bisa
  dilakukan (kata sandi salah, tujuan tidak bisa dipakai, konfigurasi
  kurang), proses ditandai gagal — tidak pernah diam-diam berpura-pura
  sudah mencadangkan sesuatu.

## 6. Berbicara bahasa yang sama dengan Restic asli

Syarat terberatnya adalah kompatibilitas: brankas buatan bqckup harus bisa
dibaca oleh program resmi Restic (dan sebaliknya untuk brankas yang sudah
ada). Setiap detail format — tata letak brankas, format peti, format
katalog, file kunci — diverifikasi terhadap kode sumber resmi Restic
sebelum diimplementasikan. Ada satu rangkaian tes khusus yang menjalankan
binari Restic asli terhadap brankas buatan bqckup: mengeceknya, mendaftar
snapshotnya, dan memulihkan file-nya byte demi byte.

Satu catatan kompatibilitas yang penting dalam praktiknya: peta folder
mencatat waktu perubahan file, tetapi sengaja **tidak** mencatat waktu
"akses terakhir", karena sekadar membaca file saat backup sudah mengubah
waktu aksesnya — yang akan membuat setiap peta folder terlihat berbeda di
setiap proses dan diam-diam menghancurkan penghematan ruang. (Restic asli
juga hanya menyimpan waktu akses bila diminta secara eksplisit, persis
karena alasan ini.)

## 7. Posisinya di dalam bqckup

Bqckup mempertahankan dua mode backup berdampingan:

- `backup_mode: full` — mode klasik yang menghasilkan arsip `.tar.gz`.
  Tidak berubah.
- `backup_mode: incremental` — mode cepat dengan deduplikasi yang selalu
  memakai mesin pure-Go bawaan bqckup. Mesin ini melayani storage lokal dan
  S3/R2 tanpa program Restic eksternal. Field `incremental.engine` sudah
  dihapus karena tidak ada lagi engine runtime kedua.

Setelah proses sukses, basis data riwayat bqckup mencatat satu entri per
tujuan, sehingga runner, aturan retensi, dan laporan status bekerja persis
seperti sebelumnya. Tidak ada bagian lain dari bqckup yang perlu diubah.

## 8. Yang sudah ada sekarang dan yang menyusul (rencananya, dalam bahasa sederhana)

Rencana dibagi menjadi tahap-tahap agar setiap bagian bisa dibangun dan
diuji sendiri-sendiri:

**Selesai:**

- Membuat dan membuka brankas lokal.
- Menyusuri folder, memotong file, deduplikasi, enkripsi, kompresi,
  penyimpanan.
- Menulis peta folder dan snapshot.
- Mendaftar snapshot.
- Membaca brankas buatannya sendiri, sekaligus brankas yang dibuat oleh
  program Restic asli (versi 2), dan bisa melanjutkan keduanya.
- Retensi menyimpan N snapshot terbaru lalu melakukan mark-and-sweep prune
  untuk mereklamasi pack yang tidak lagi dipakai.
- Penyimpanan lokal dan S3/R2.
- Lock kompatibel Restic untuk mencegah writer bersamaan dan command unlock
  untuk membersihkan lock usang.
- Memverifikasi semuanya terhadap binari Restic asli.

**Tahap berikutnya:**

- **Masa depan — Pemulihan (restore):** mengembalikan file keluar dari
  brankas. Aturan yang sudah dikunci: pemulihan selalu butuh tujuan
  eksplisit dan tidak pernah menimpa file yang ada secara diam-diam.

**Sengaja di luar cakupan selamanya:** antarmuka web, daemon latar
belakang, dan fork program Restic selain yang satu ini.

## 9. Cara pengujiannya

- **Tes unit** untuk setiap bagian kecil: pemotong, enkripsi, peti,
  katalog, peta folder.
- **Tes pulang-pergi** yang membuat brankas, mencadangkan folder sungguhan
  (termasuk file besar, file kosong, dan symbolic link), mencadangkan lagi,
  dan memeriksa bahwa proses kedua hampir tidak menyimpan hal baru.
- **Tes kompatibilitas** yang menyerahkan brankas ke program Restic asli
  dan menuntut: "periksa, daftarkan, pulihkan — dan file yang dipulihkan
  harus identik."
- **Tes keamanan** untuk pembatalan, kata sandi salah, dan input rusak.

## 10. Tempat spesifikasi aslinya

| Pertanyaan sederhana | Dokumen teknis |
| --- | --- |
| Desain lengkap | `docs/superpowers/specs/2026-08-20-restic-engine-phase1-design.md` |
| Aturan format persis, diverifikasi terhadap sumber Restic | `docs/superpowers/notes/restic-format-verification.md` |
| Keputusan produk (yang akan/tidak akan dilakukan) | `docs/superpowers/notes/restic-product-decisions.md` |
| Desain keamanan backup | `docs/superpowers/notes/restic-threat-model.md` |
| Pendekatan pengujian | `docs/superpowers/notes/restic-test-strategy.md` |
| Milestone dan status | `docs/intern-backlog.md` (M11 dan seterusnya) |
| Arsitektur bqckup keseluruhan | `docs/architecture.md` |
