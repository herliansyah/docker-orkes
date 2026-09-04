# Rincian Fitur & Arsitektur Docker Orkes

Dokumen ini menjelaskan rancangan arsitektur, model operasional, dan kapabilitas teknis mendalam dari platform **Docker Orkes**.

---

## 1. Prinsip Utama

- **Kesederhanaan Tanpa Agen (Agentless)**: Tidak ada agen atau daemon khusus yang dipasang di server target. Seluruh komunikasi dikendalikan via SSH standar menggunakan library murni Go (`golang.org/x/crypto/ssh`).
- **Stack Deklaratif**: Setiap aplikasi didefinisikan sebagai template reusable Docker Compose yang modular.
- **Single-Binary Footprint**: Control Plane berjalan sebagai satu binary Go mandiri yang membungkus database SQLite serta asset UI (`embed.FS`).
- **Auditabilitas Penuh via Git Mirror**: Seluruh perubahan manifest stack, variabel deployment, dan inventaris server dicatat ke dalam audit log Git berversi.

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

## 4. Live Remote SFTP Code Editor

Setiap deployment memiliki editor file terintegrasi langsung di web browser:
- **Jelajahi File**: Melihat semua file dalam folder `~/.docker-orkes/stacks/<stack-name>/`.
- **Live Code Editor**: Menampilkan dan menyunting isi file `docker-compose.yml` atau `.env` secara langsung.
- **Save & Redeploy**: Menyimpan file via SFTP dan langsung mengeksekusi `docker compose up -d` di server tujuan dalam satu kali klik.

---

## 5. Observabilitas Host & Telemetri

Pada halaman detail node (`/nodes/<id>`), Docker Orkes mengambil data telemetri secara berkala tanpa membebani server:
- **Kapasitas Penyimpanan Disk**: Memeriksa `df -h /` untuk menghitung kapasitas total, ruang terpakai, ruang bebas, dan persentase penggunaan.
- **Vital Docker Engine**: Memeriksa informasi engine dan status container aktif via `docker ps -a` untuk menampilkan port yang terbuka dan status kesehatan container.

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
