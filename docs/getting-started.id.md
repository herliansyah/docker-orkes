# Panduan Memulai Docker Orkes

Panduan ini memandu Anda mulai dari persiapan, instalasi, pendaftaran server, hingga deployment aplikasi multi-node pertama Anda menggunakan **Docker Orkes**.

---

## 1. Prasyarat

Sebelum memulai, pastikan Anda telah menyiapkan:
1. **Host Control Plane**: Komputer lokal atau server VPS (Linux / macOS) dengan Go 1.22+ jika ingin mengompilasi sendiri.
2. **Server Target (Node)**: Satu atau lebih server Linux remote yang memenuhi syarat:
   - Telah terpasang Docker Engine & plugin compose (`docker compose version` dapat dijalankan).
   - Layanan SSH aktif dan portnya dapat diakses dari server Control Plane.
   - Akun pengguna dengan hak akses menjalankan Docker (user `root` atau anggota grup `docker`).

---

## 2. Instalasi dan Menjalankan

### Kompilasi dari Source
```bash
git clone https://github.com/herliansyah/docker-orkes.git
cd docker-orkes
go build -o bin/docker-orkes ./cmd/docker-orkes
```

### Menjalankan Control Plane
```bash
./bin/docker-orkes -addr :8080 -pass password-rahasia-anda
```

Atau menggunakan variabel lingkungan (*environment variables*):
```bash
export ADDR=":8080"
export MASTER_PASSWORD="password-rahasia-anda"
export DB_PATH="docker-orkes.db"
export LOG_FILE="docker-orkes.log"
export LOG_LEVEL="info"
./bin/docker-orkes
```

Akses `http://localhost:8080` melalui browser Anda dan login dengan password yang telah Anda masukkan.

---

## 3. Otorisasi Server Remote (Node)

Saat pertama kali dijalankan, Docker Orkes otomatis membuat pasangan kunci SSH RSA 4096-bit di database (`docker-orkes.db`).

Anda dapat mendaftarkan dan mengotorisasi server node melalui salah satu dari 2 cara berikut:

### Opsi A: Injeksi Otomatis via Password (Direkomendasikan)
1. Buka tab **Managed Nodes** di dashboard.
2. Klik tombol **+ Add Node**.
3. Isi nama server, IP/hostname, port SSH (default 22), dan user SSH (misal `root`).
4. Pada kolom **⚡ Auto-Inject SSH Key via Password**, masukkan password SSH server tujuan.
5. Klik **Save & Setup Node**. Docker Orkes akan terhubung sekali, memasang public key ke `~/.ssh/authorized_keys`, dan langsung memvalidasi koneksi. Password ini **tidak disimpan** di database.

### Opsi B: Otorisasi Public Key Manual
1. Klik tombol **⚙️ Settings** di navigasi kanan atas.
2. Klik tombol **Copy Key** pada kartu **Control Plane SSH Public Key**.
3. Login ke server target Anda, lalu tempelkan key tersebut ke `~/.ssh/authorized_keys`:
   ```bash
   echo "ssh-rsa AAAA..." >> ~/.ssh/authorized_keys
   chmod 600 ~/.ssh/authorized_keys
   ```
4. Di dashboard, klik **+ Add Node**, isi data node tanpa mengisi password, lalu klik **Save & Setup Node**.
5. Buka menu Actions pada node tersebut lalu klik **Test Connection** untuk memverifikasi.

---

## 4. Membuat dan Menyunting Template Stack

1. Buka tab **Stack Templates**.
2. Klik tombol **+ Add Stack** (atau klik baris stack yang ada untuk membuka **Stack Template Editor**).
3. Masukkan nama slug template (misal `web-proxy` atau `redis-cache`).
4. Masukkan definisi `docker-compose.yml` langsung di editor kode terintegrasi ala VS Code:
   ```yaml
   services:
     web:
       image: nginx:alpine
       ports:
         - "${PORT:-80}:80"
       restart: always
   ```
   > [!TIP]
   > **Fitur Editor & Validasi**:
   > - **Tema Gelap VS Code**: Penyorotan sintaks berwarna lengkap untuk YAML dan file `.env`.
   > - **Ergonomi Pengetikan**: Nomor baris, indentasi otomatis 2 spasi saat menekan tombol `Tab`, dan pencocokan kurung siku/kurawal.
   > - **Perbesar Ruang Kerja (*Maximize*)**: Klik tombol **Maximize** (`⛶`) pada header modal (atau tekan `Esc` untuk memulihkan) untuk memperbesar editor hingga memenuhi seluruh layar desktop.
   > - **Proteksi Validasi Format**: Saat mengklik **Save Stack Template**, sistem memeriksa sintaks YAML dan kewajiban blok `services:`. Jika terdapat kesalahan format, baris error langsung diberi sorotan merah tanpa menghilangkan ketikan Anda.
5. Isi default environment variable (misal `PORT=8080`) dan catatan opsional pada tab terkait.
6. Klik **Save Stack Template**.

---

## 5. Menjalankan Deployment ke Node

1. Buka tab **Active Deployments**.
2. Klik tombol **+ New Deployment**.
3. Pilih template stack dan server target yang diinginkan.
4. Sesuaikan variabel lingkungan `.env` khusus untuk server tersebut.
5. Klik **Deploy to Node**.

Docker Orkes akan secara otomatis:
- Terhubung ke server via SFTP.
- Membuat direktori stack `~/.docker-orkes/stacks/<stack-name>/`.
- Mengunggah file `docker-compose.yml` dan `.env`.
- Menjalankan `docker compose up -d`.
- Mencatat status, hash Desired State, dan entri snapshot revisi awal di database.

> [!WARNING]
> **Aturan Penting Persistensi Data**: Direktori stack `~/.docker-orkes/stacks/<stack-name>/` dialokasikan khusus untuk template compose, file `.env`, dan konfigurasi pendukung yang bersifat ringan. **Jangan pernah menyimpan direktori database atau data persisten produksi (seperti `./data:/var/lib/mysql`) di dalam direktori stack ini**, karena folder tersebut akan dihapus total (`rm -rf`) jika operator memilih opsi "Purge Data" saat menghapus deployment. Selalu gunakan Docker Named Volumes atau bind-mount path absolut di luar `~/.docker-orkes/` (misal `/data/...` atau `/mydocker/...`) untuk data persisten.

---

## 6. Mengaktifkan Backup Git Mirror (Opsional)

1. Buka modal **⚙️ Settings**.
2. Pada bagian **Git Mirror Backup Sync**, masukkan URL repositori Git Anda:
   - SSH: `git@github.com:organisasi/infra-backup.git`
   - HTTPS: `https://github.com/organisasi/infra-backup.git`
3. Tentukan nama branch (misal `main`).
4. Jika menggunakan SSH, salin Control Plane SSH Public Key dan tambahkan sebagai **Deploy Key dengan akses Write** di repositori GitHub/GitLab Anda.
5. Klik tombol **Save & Sync Mirror**. Docker Orkes akan melakukan commit dan push seluruh konfigurasi secara otomatis.

---

## 7. Mengelola File Remote & Runtime Assets

1. Pada tabel **Active Deployments**, klik tombol **📁 Files** pada salah satu baris deployment.
2. Jelajahi struktur file dan folder di dalam direktori `~/.docker-orkes/stacks/<stack-name>/`.
3. Buat file baru (misal `nginx.conf`) atau subdirektori melalui tombol `+File` / `+Folder`.
4. Unggah berkas atau folder bersarang sekaligus menggunakan tombol **Upload** atau fitur *drag-and-drop*.
5. Unggah arsip `.tar.gz` atau `.zip`, lalu klik tombol **Extract** untuk mengekstrak aset di server remote.
6. Sunting file teks langsung di editor web dan klik **Save & Redeploy** untuk memuat ulang kontainer seketika.

---

## 8. Membuka Web Terminal Interaktif (PTY)

1. **Terminal Host**:
   - Pada tab **Managed Nodes**, klik tombol **Terminal** pada kartu server tujuan.
   - Sesi terminal browser berlatensi rendah akan terhubung via WebSocket langsung ke shell sistem operasi remote host.
2. **Terminal Kontainer**:
   - Pada tab **Active Deployments** atau halaman **Node Detail**, buka menu dropdown **Terminal** lalu pilih **Container Terminal**.
   - Pilih kontainer target; Docker Orkes membuka sesi interaktif langsung di dalam kontainer (`docker exec -it /bin/sh` atau `/bin/bash`).

---

## 9. Riwayat Revisi Deployment & Rollback Instan

1. Pada tab **Active Deployments**, klik tombol **📜 Revisions** pada deployment yang diinginkan.
2. Tinjau rekaman snapshot konfigurasi compose, nilai variabel lingkungan, dan timestamp sebelumnya.
3. Klik tombol **Rollback** pada revisi yang ingin dipulihkan. Sistem akan membuat entri revisi baru (*forward-only*) dan langsung mendeploykannya ke server host.

---

## 10. Konfigurasi Notifikasi & Poller Latar Belakang

1. Klik tombol **⚙️ Settings** di bilah navigasi atas.
2. Pada bagian **Liveness Poller Settings**, tentukan interval polling (misal `2m`).
3. Pada bagian **Notification Channels**, pilih platform notifikasi Anda:
   - **Telegram**: Masukkan Bot Token dan Chat ID penerima.
   - **Discord**: Masukkan Webhook URL dari channel Discord Anda.
   - **Custom Webhook**: Masukkan URL HTTP endpoint yang menerima payload JSON.
4. Klik tombol **Send Test Alert** untuk memastikan notifikasi terkirim, lalu simpan pengaturan.

---

## 11. Memantau In-App Activity Console & Live Telemetry Stream

Untuk melihat seluruh operasi internal Control Plane, eksekusi SSH, dan audit kesehatan sistem secara langsung dari browser tanpa perlu membuka terminal server:
1. Klik tombol **⚡ Activity** di status bar footer bagian bawah (atau tekan tombol pintas `Ctrl+``` di layar mana saja).
2. Drawer interaktif 6 kolom Activity Console akan terbuka:
   - **Live Telemetry Stream**: Memantau event dari 7 modul inti (`poller`, `ssh`, `deploy`, `gitsync`, `notifier`, `auth`, `server`).
   - **Jeda & Lanjutkan (Pause & Resume)**: Klik **⏸ Pause** untuk menahan guliran log saat Anda sedang menganalisis insiden tertentu tanpa memutus koneksi streaming.
   - **Smart Auto-Scroll**: Tampilan otomatis bergulir mengikuti log terbaru, namun otomatis membeku saat Anda men-scroll ke atas untuk meninjau log lama.
   - **Filter & Pencarian**: Saring menurut tingkat keparahan (`ALL`, `ERROR`, `WARN`, `INFO`, `DEBUG`) atau gunakan kotak pencarian instan untuk memfilter menurut nama node, modul, atau teks pesan.
   - **Inspeksi Detail**: Klik baris log yang memiliki metadata untuk membuka rincian atribut JSON terstruktur.
   - **Ekspor Log**: Klik **💾 Export** untuk mengunduh rekaman log browser saat itu juga ke dalam format teks `.log` atau data `.json`.

---

## 12. Mengaudit Image Kontainer & Pemindaian Kerentanan (CVE)

Untuk mengaudit dan menambal celah keamanan kontainer di seluruh server node:
1. Masuk ke tab **Managed Nodes** dan klik pada server node untuk membuka halaman rincian (`/nodes/<id>`).
2. Buka tab **🖼️ Images**.
3. **Pemindaian Satu Image (Single Scan)**:
   - Cari image yang ingin diaudit pada tabel.
   - Klik tombol **🛡️ Scan** pada kolom Actions.
   - Control Plane akan menjalankan scanner Trivy sementara di server target, lalu menyimpan metrik CVE dan timestamp ke database SQLite.
4. **Proteksi Cooldown 24 Jam & Force Scan**:
   - Setelah scan berhasil, tombol berubah menjadi **🛡️ Force** disertai countdown waktu tunggu (contoh: `Next: ~22h`).
   - Scan berkala otomatis ditiadakan selama masa cooldown untuk menjaga CPU dan kuota bandwidth. Gunakan tombol **Force** hanya jika Anda baru saja mem-build/meng-update image baru dan butuh validasi langsung saat itu juga.
5. **Bulk Scanning (Scan All CVEs)**:
   - Klik tombol **🛡️ Scan All CVEs** pada toolbar atas tabel image.
   - Control Plane secara berurutan (*sequential*) memindai setiap image di background dengan jeda aman, dan secara otomatis melewati (*skip*) image yang masih dalam cooldown 24 jam.
   - Anda dapat memantau progres persentase melalui banner di atas tabel dan event log langsung di Activity Console.
6. **Melihat Rincian Laporan Kerentanan**:
   - Klik pada badge kerentanan baris image (contoh: `1 Crit 3 High` atau `✓ Clean`).
   - Modal **Vulnerability Report** akan menampilkan seluruh daftar CVE ID, nama paket software yang terdampak, versi yang sedang terpasang, serta versi perbaikan (*fixed version*), lengkap dengan opsi tab *View Raw JSON*.

