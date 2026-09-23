# Rincian Fitur & Arsitektur Docker Orkes

Dokumen ini menjelaskan rancangan arsitektur, model operasional, dan kapabilitas teknis mendalam dari platform **Docker Orkes**.

---

## 1. Prinsip Utama

- **Kesederhanaan Tanpa Agen (Agentless)**: Tidak ada agen atau daemon khusus yang dipasang di server target. Seluruh komunikasi dikendalikan via SSH standar menggunakan library murni Go (`golang.org/x/crypto/ssh`).
- **Stack Deklaratif**: Setiap aplikasi didefinisikan sebagai template reusable Docker Compose yang modular.
- **Single-Binary Footprint**: Control Plane berjalan sebagai satu binary Go mandiri yang membungkus database SQLite serta asset UI (`embed.FS`).
- **Auditabilitas Penuh via Git Mirror**: Seluruh perubahan manifest stack, variabel deployment, dan inventaris server dicatat ke dalam audit log Git berversi.
- **Batasan Keamanan Pragmatis**: Pemindaian kerentanan (*vulnerability scan*) adalah domain pipeline CI/CD saat build atau container registry, bukan membebani control plane deployment dengan database CVE yang rapuh dan dependensi internet eksternal.

---

## 2. Struktur File Stack pada Server Remote

Pada server node yang dikelola, Docker Orkes meletakkan aplikasi dalam jalur direktori yang bersih dan terisolasi:

```
~/.docker-orkes/
└── stacks/
    ├── web-proxy/
    │   ├── docker-compose.yml
    │   └── .env
    └── redis-cache/
        ├── docker-compose.yml
        └── .env
```

---

## 3. Deteksi dan Rekonsiliasi Configuration Drift

### Apa itu Drift?
*Drift* terjadi jika file konfigurasi di server remote (*Actual State*) berbeda dari konfigurasi yang tercatat di Control Plane (*Desired State*). Misalnya, ketika operator melakukan SSH langsung ke server lalu menyunting file `docker-compose.yml` atau `.env` secara manual.

### Cara Kerja Deteksi Drift:
1. Saat operator menekan tombol **Check Drift**, Docker Orkes menghubungi server remote via SFTP.
2. Mengambil isi file `docker-compose.yml` dan `.env` yang berada di server.
3. Membandingkan dan menghasilkan diff terpadu (line-by-line unified diff).
4. Jika ditemukan perbedaan, status deployment berubah menjadi **⚠️ Drifted** dan menampilkan laporan perbedaan secara transparan.

### Aksi Penyelesaian Drift:
- **Reconcile (Overwrite Remote)**: Control Plane mengirim ulang Desired State dari database dan menjalankan `docker compose up -d` untuk memastikan kepatuhan konfigurasi.
- **Adopt Remote**: Control Plane menyerap perubahan aktual dari server remote menjadi Desired State baru dan menyimpannya ke Git Mirror.

---

## 4. Remote Stack Filesystem & Runtime Assets

Setiap deployment aktif menyediakan pengelola sistem berkas remote yang andal pada path `~/.docker-orkes/stacks/<stack-name>/`:
- **Pohon Direktori & Breadcrumbs**: Navigasi subfolder secara visual dengan jalur remah roti (*breadcrumbs*) yang interaktif.
- **Manajemen File & Folder**: Membuat file baru dan subdirektori langsung di server tujuan via SFTP.
- **Upload Drag-and-Drop & Folder Rekursif**: Mengunggah file tunggal, banyak file sekaligus, atau seluruh struktur folder bersarang dengan mempertahankan path relatifnya (`webkitdirectory`).
- **Ekstraksi Arsip Remote**: Mengekstrak arsip `.tar.gz` dan `.zip` langsung di server tujuan menggunakan utility bawaan sistem operasi Linux host (`tar`/`unzip`).
- **Live Code Editor & Masking Secrets**: Menyunting file teks/kode secara instan, menyamarkan (*mask*) nilai kredensial sensitif pada `.env`, dan mengeksekusi **Save & Redeploy** (`docker compose up -d`) dalam satu kali klik.
- **Isolasi Drift**: File selain `docker-compose.yml` dan `.env` diklasifikasikan sebagai *Runtime Assets* (misal: file konfigurasi aplikasi, sertifikat SSL) dan dikecualikan dari deteksi drift konfigurasi ([ADR 0008](adr/0008-remote-stack-filesystem-and-runtime-assets.md)).
- **Batasan Storage Persisten & Pengaman Purge**: Stack Directory bersifat *ephemeral* (sementara) dan hanya untuk konfigurasi deklaratif. Database dan data persisten wajib menggunakan Docker Named Volumes atau host path eksternal ([ADR 0016](adr/0016-stack-directory-isolation-and-data-purge-guardrails.md)). Guardrail otomatis 50 MB memblokir `rm -rf` jika terdeteksi data berukuran besar.

---

## 5. Observabilitas Host, Telemetri & Log Kontainer

Pada halaman detail node (`/nodes/<id>`), Docker Orkes mengambil data telemetri secara berkala tanpa membebani server:
- **Kapasitas Penyimpanan Disk**: Memeriksa `df -h /` untuk menghitung kapasitas total, ruang terpakai, ruang bebas, dan persentase penggunaan secara visual.
- **Vital Docker Engine**: Memeriksa informasi engine dan status container aktif via `docker ps -a` untuk menampilkan port yang terbuka dan status kesehatan container.
- **Live Container Logs**: Membaca streaming log stdout/stderr langsung dari kontainer yang sedang berjalan melalui eksekusi non-intrusif `docker logs --tail 200`.

---

## 6. Arsitektur Backup Git Mirror

Docker Orkes mengintegrasikan client Git murni (`go-git`):
1. Mengelola repositori lokal di host Control Plane pada folder `data/git-mirror/`.
2. Setiap kali ada perubahan (tambah stack, edit stack, deploy baru, update catatan), sistem memperbarui struktur Git:
   ```
   git-mirror/
   ├── inventory.json
   ├── RUNBOOK.md
   ├── stacks/
   │   └── <stack-name>/docker-compose.yml
   └── deployments/
       └── <node-name>/<stack-name>.env
   ```
3. Saat proses sinkronisasi dipicu, Docker Orkes membuat commit otomatis dan melakukan push ke upstream Git (GitHub, GitLab, Gitea).

---

## 7. Deployment Revisions & Instant Rollback

Untuk mencegah kesalahan konfigurasi atau kegagalan versi rilis:
- **Snapshot Immutable**: Setiap deployment baru otomatis menyimpan snapshot isi `docker-compose.yml`, variabel lingkungan `.env`, dan timestamp sebagai entri revisi berversi ([ADR 0005](adr/0005-forward-only-deployment-revisions-with-bounded-retention.md)).
- **Forward-Only Rollback**: Operasi rollback ke revisi terdahulu tidak pernah menghapus riwayat audit; sistem akan membuat revisi baru (*forward-only*) dari konfigurasi lama tersebut dan langsung mendeploykannya ke server tujuan.
- **Retensi Terbatas**: Kapasitas riwayat revisi dibatasi (default: 20 revisi per deployment) untuk menjaga efisiensi database SQLite.

---

## 8. Poller Latar Belakang Otomatis & Notifikasi Multi-Kanal

Tanpa perlu operator terus memantau dashboard secara manual:
- **Background Poller**: Poller ringan memeriksa keterjangkauan node, kesehatan kontainer, dan perubahan drift secara otomatis pada interval yang dapat dikonfigurasi (misal tiap 2 menit) tanpa membebani server ([ADR 0004](adr/0004-lightweight-poller-and-edge-triggered-alerting.md)).
- **Peringatan Berbasis Transisi (Edge-Triggered)**: Notifikasi hanya dikirim saat terjadi transisi status (misal dari `online` menjadi `offline`, atau kontainer crash), mencegah spam dan *alert fatigue*.
- **Integrasi Kanal**: Mendukung pengiriman peringatan langsung ke Telegram Bot, Discord Webhook, maupun Custom JSON HTTP Webhook melalui pengaturan Control Plane.

---

## 9. Web Terminal Interaktif 100% Offline (PTY)

Operator dapat membuka sesi terminal Linux berkecepatan tinggi langsung dari web browser tanpa ketergantungan internet maupun layanan SaaS pihak ketiga ([ADR 0009](adr/0009-offline-web-terminal-pty.md)):
- **Dua Mode Target**:
  1. **Host Terminal**: Membuka sesi interaktif PTY SSH langsung ke shell host node.
  2. **Container Terminal**: Membuka sesi interaktif PTY langsung di dalam kontainer yang berjalan (`docker exec -it <container> /bin/sh`) dengan deteksi otomatis `/bin/bash` atau `/bin/sh` (Alpine/BusyBox).
- **Aset Klien Bundled Lokal**: Emulasi terminal digerakkan oleh library `xterm.js` dan `addon-fit` lokal yang di-embed langsung ke dalam binary Go (`embed.FS`).
- **Keamanan & Siklus Sesi**: Sesi terminal dilengkapi pemutus otomatis saat *idle* selama 30 menit, sanitasi ketat ID kontainer untuk mencegah *shell injection*, dan sinkronisasi ukuran layar (`SIGWINCH`) secara real-time melalui WebSocket.

---

## 10. Disaster Recovery & Replikasi Warm Standby

Docker Orkes mendukung arsitektur pencadangan **Active-Passive (Warm Standby)** ([ADR 0006](adr/0006-active-passive-control-plane-replication.md)):
- **Node Berjalan Mandiri**: Kontainer pada remote host beroperasi sepenuhnya independen dari Control Plane. Apabila server Control Plane utama mengalami kendala, seluruh kontainer aplikasi tetap berjalan normal.
- **Kunci SSH Terpadu**: SSH key Control Plane tersimpan di database SQLite, sehingga replikasi database otomatis mendistribusikan kunci akses tanpa perlu mengubah `~/.ssh/authorized_keys` di remote node.
- **Bebas Split-Brain**: Server standby dibiarkan mati (*stopped*) dan hanya dihidupkan saat promosi darurat, memastikan poller tidak berjalan ganda. Panduan langkah demi langkah tersedia di [Panduan Disaster Recovery](disaster-recovery.id.md).

---

## 11. Arsitektur Internal Hybrid Image Registry

Untuk lingkungan jaringan terisolasi (*air-gapped*) atau server dengan keterbatasan *bandwidth*, Docker Orkes menyediakan pola integrasi OCI Registry internal ([ADR 0007](adr/0007-internal-hybrid-image-registry.md)):
- Dijalankan sebagai template Docker Compose standar di salah satu node yang ditunjuk.
- Berfungsi ganda sebagai penyimpanan image lokal kustom dan *pull-through cache* untuk upstream registry (Docker Hub, GitHub Container Registry) guna mempercepat proses deployment di seluruh cluster node.
- Panduan implementasi teknis dan konfigurasi lengkap tersedia di [Panduan Setup Registry Internal](internal-registry.id.md).
