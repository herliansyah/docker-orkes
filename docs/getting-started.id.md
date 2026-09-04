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
go build -o docker-orkes ./cmd/docker-orkes
```

### Menjalankan Control Plane
```bash
./docker-orkes -addr :9000 -pass password-rahasia-anda
```

Akses `http://localhost:9000` melalui browser Anda dan login dengan password yang telah Anda masukkan.

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

## 4. Membuat Template Stack

1. Buka tab **Stack Templates**.
2. Klik tombol **+ Add Stack**.
3. Masukkan nama slug template (misal `web-proxy` atau `redis-cache`).
4. Masukkan definisi `docker-compose.yml`:
   ```yaml
   services:
     web:
       image: nginx:alpine
       ports:
         - "${PORT:-80}:80"
       restart: always
   ```
5. Isi default environment variable (misal `PORT=8080`) dan catatan opsional.
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
- Mencatat status dan hash Desired State di database.

---

## 6. Mengaktifkan Backup Git Mirror (Opsional)

1. Buka modal **⚙️ Settings**.
2. Pada bagian **Git Mirror Backup Sync**, masukkan URL repositori Git Anda:
   - SSH: `git@github.com:organisasi/infra-backup.git`
   - HTTPS: `https://github.com/organisasi/infra-backup.git`
3. Tentukan nama branch (misal `main`).
4. Jika menggunakan SSH, salin Control Plane SSH Public Key dan tambahkan sebagai **Deploy Key dengan akses Write** di repositori GitHub/GitLab Anda.
5. Klik tombol **Save & Sync Mirror**. Docker Orkes akan melakukan commit dan push seluruh konfigurasi secara otomatis.
