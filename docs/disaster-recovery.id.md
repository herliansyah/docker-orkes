# Disaster Recovery & Standby Control Plane

Dokumen ini menjelaskan arsitektur pemulihan bencana (*disaster recovery*) dan panduan operasional (*runbook*) untuk Docker Orkes menggunakan konfigurasi **Active-Passive (Warm Standby)**.

---

## 1. Ikhtisar Arsitektur

```
+------------------------------------+             +------------------------------------+
|       PRIMARY CONTROL PLANE        |             |       STANDBY CONTROL PLANE        |
|  - Host IP: 192.168.1.10           |             |  - Host IP: 192.168.1.20           |
|  - docker-orkes: BERJALAN          |             |  - docker-orkes: MATI (STOPPED)    |
|  - Poller: AKTIF (tiap 2 menit)    |             |  - Poller: TIDAK AKTIF             |
|  - Database: docker-orkes.db       |             |  - Database: docker-orkes.db (sync)|
+-----------------+------------------+             +------------------^-----------------+
                  |                                                   |
                  | (Tiap 15 menit via cron)                          |
                  | 1. sqlite3 .backup -> temp.db                     |
                  +----------------------- rsync -az -----------------+
                                          |
                                          v
                              +-----------------------+
                              |      REMOTE NODE      |
                              |  - Kontainer jalan    |
                              |  - authorized_keys    |
                              +-----------------------+
```

### Prinsip Utama

1. **Aplikasi di Node Berjalan Otonom**:
   Sesuai prinsip agentless SSH ([ADR 0001](adr/0001-agentless-ssh-architecture.md)), kontainer Docker di remote Node berjalan mandiri dan tidak bergantung pada status Control Plane. Jika server Control Plane mati, seluruh aplikasi pengguna tetap berjalan tanpa gangguan.

2. **Identitas Kriptografi Terpadu**:
   Pasangan SSH Key Control Plane disimpan langsung di dalam database SQLite (tabel `settings` via `EnsureSSHKeyPair`). Replikasi database otomatis menyertakan private key yang sama persis. **Anda TIDAK PERLU mengubah `~/.ssh/authorized_keys` pada remote Node.**

3. **Pencegahan Split-Brain**:
   Service `docker-orkes` di server Standby harus **tetap mati (`stopped`)** hingga dipromosikan secara manual. Ini mencegah dua Poller berjalan bersamaan yang dapat memicu notifikasi ganda ke Telegram/Discord atau konflik SSH.

4. **Konfigurasi Firewall**:
   Jika remote Node menggunakan firewall (seperti UFW atau Security Group) yang membatasi akses port SSH (port 22) ke IP tertentu, pastikan **IP server Standby juga dimasukkan ke daftar whitelist** di setiap remote Node.

---

## 2. Pengaturan Sinkronisasi (Primary -> Standby)

### Langkah 1: Siapkan Akses SSH dari Primary ke Standby
Pada server **Primary**, pastikan ada SSH key khusus agar bisa mengakses server **Standby** tanpa meminta password interaktif:

```bash
# Buat SSH key baru di Primary (jika belum ada)
ssh-keygen -t ed25519 -f ~/.ssh/id_dr_backup -N ""

# Salin public key ke server Standby
ssh-copy-id -i ~/.ssh/id_dr_backup.pub orkes-backup@standby-host
```

### Langkah 2: Buat Skrip Backup & Sync di Primary
Buat file `/usr/local/bin/orkes-sync-standby.sh` di server **Primary**:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Konfigurasi
DATA_DIR="/opt/docker-orkes"
DB_FILE="${DATA_DIR}/docker-orkes.db"
STANDBY_USER="orkes-backup"
STANDBY_HOST="192.168.1.20"
STANDBY_DIR="/opt/docker-orkes"
SSH_KEY="/home/orkes/.ssh/id_dr_backup"
TMP_BACKUP="/tmp/docker-orkes-backup.db"

# 1. Non-blocking SQLite online backup (aman meskipun DB sedang dipakai dan mode WAL)
sqlite3 "${DB_FILE}" ".backup '${TMP_BACKUP}'"

# 2. Transfer snapshot ke server Standby
rsync -az -e "ssh -i ${SSH_KEY} -o StrictHostKeyChecking=accept-new" \
  "${TMP_BACKUP}" \
  "${STANDBY_USER}@${STANDBY_HOST}:${STANDBY_DIR}/docker-orkes.db"

# 3. Hapus snapshot sementara
rm -f "${TMP_BACKUP}"
```

Beri izin eksekusi:
```bash
chmod +x /usr/local/bin/orkes-sync-standby.sh
```

### Langkah 3: Pasang Cron Job di Primary
Tambahkan jadwal cron untuk berjalan setiap 15 menit:

```bash
crontab -e
```

Masukkan baris berikut:
```cron
*/15 * * * * /usr/local/bin/orkes-sync-standby.sh > /var/log/orkes-sync.log 2>&1
```

---

## 3. Panduan Failover Darurat (Promosi Standby)

Saat server Primary mengalami kegagalan total, kerusakan hardware, atau jaringan mati:

### Langkah 1: Pastikan Server Primary Benar-benar Down
Pastikan Primary sudah tidak dapat diakses agar tidak terjadi kerancuan:
```bash
ping -c 3 192.168.1.10 || echo "Primary offline"
```

### Langkah 2: Nyalakan Control Plane di Server Standby
Masuk ke server **Standby** (`192.168.1.20`) via SSH dan nyalakan service:

**Jika menggunakan systemd:**
```bash
sudo systemctl start docker-orkes
sudo systemctl status docker-orkes
```

**Jika menggunakan Docker Compose:**
```bash
cd /opt/docker-orkes
docker compose up -d
docker compose logs -f
```

### Langkah 3: Alihkan Akses Jaringan / DNS
- **Jika menggunakan Domain** (misal `orkes.internal.corp`): Ubah DNS A Record dari `192.168.1.10` menjadi `192.168.1.20`.
- **Jika menggunakan IP Langsung**: Buka `http://192.168.1.20:8080` di browser.

### Langkah 4: Verifikasi Koneksi ke Node
1. Login ke dashboard di server Standby.
2. Buka menu **Nodes**. Semua node yang terdaftar akan muncul lengkap.
3. Klik tombol **Sync All** atau tunggu background Poller berjalan (dalam 2 menit). Status node akan berubah menjadi `online`.

---

## 4. Pemulihan Pasca Insiden (Failback ke Primary)

Setelah server Primary yang asli berhasil diperbaiki atau diganti:

1. **Hentikan** service `docker-orkes` di server Standby:
   ```bash
   sudo systemctl stop docker-orkes
   ```
2. Salin database terbaru kembali ke server Primary:
   ```bash
   sqlite3 /opt/docker-orkes/docker-orkes.db ".backup /tmp/failback.db"
   rsync -az /tmp/failback.db orkes@192.168.1.10:/opt/docker-orkes/docker-orkes.db
   rm -f /tmp/failback.db
   ```
3. Nyalakan kembali `docker-orkes` di server Primary:
   ```bash
   sudo systemctl start docker-orkes
   ```
4. Aktifkan kembali cron job 15 menit di server Primary.
5. Kembalikan DNS ke IP server Primary.
