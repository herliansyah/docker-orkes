# Panduan Pengaturan Internal Hybrid Image Registry

Panduan ini memberikan instruksi langkah demi langkah untuk men-deploy dan mengonfigurasi OCI container image registry internal yang ringan menggunakan **Zot** pada platform Docker Orkes, sesuai dengan spesifikasi [ADR 0007](adr/0007-internal-hybrid-image-registry.md).

---

## 1. Ikhtisar & Arsitektur

Pada lingkungan cluster multi-node, server-server node sering kali mengunduh image yang sama berulang kali (misalnya `nginx`, `postgres`, `redis`, atau base OS image). Mengunduh langsung dari Docker Hub secara berulang menimbulkan kendala:
- Batasan penarikan anonim Docker Hub (*rate limit* 100 pull per 6 jam).
- Pembengkakan biaya dan konsumsi *bandwidth* internet keluar.
- Tingginya latensi unduhan antar-server remote.

Docker Orkes mengadopsi **Zot** (`ghcr.io/project-zot/zot-linux-amd64`) yang di-deploy sebagai Template Stack standar pada salah satu Node infrastruktur.

```
                    ┌────────────────────────┐
                    │  Upstream Registries   │
                    │ (Docker Hub / GHCR)    │
                    └───────────┬────────────┘
                                │ (On-demand pull-through)
                                ▼
                    ┌────────────────────────┐
                    │  Registry Node (:5000) │
                    │   Zot OCI Registry     │
                    │  - Repositori Privat   │
                    │  - Pull-Through Cache  │
                    └───────┬────────┬───────┘
            Pull Lokal      │        │    Pull Lokal
        ┌───────────────────┘        └───────────────────┐
        ▼                                                ▼
┌─────────────────┐                            ┌─────────────────┐
│     Node 1      │                            │     Node 2      │
│  (Docker Host)  │                            │  (Docker Host)  │
│  daemon.json -> │                            │  daemon.json -> │
│  registry-mirror│                            │  registry-mirror│
└─────────────────┘                            └─────────────────┘
```

### Keunggulan Utama
1. **Sederhana (Single-Container)**: Zot berjalan sebagai satu kontainer binary mandiri yang hanya membutuhkan memori RAM ~30MB. Tidak memerlukan database terpisah, Redis, ataupun background worker tambahan.
2. **Fungsi Ganda (Hybrid)**:
   - **Pull-Through Cache**: Otomatis menyimpan cache image dari Docker Hub saat pertama kali ditarik. Tarikan berikutnya oleh node mana pun akan diambil langsung dari penyimpanan lokal dengan kecepatan LAN (1–10 Gbps).
   - **Penyimpanan Image Privat**: Menjadi target push/pull utama untuk image microservice kustom internal Anda dari pipeline CI/CD.
3. **100% Agentless & Deklaratif**: Dikelola penuh sebagai Stack Docker Orkes dengan file yang tertata rapi di direktori `~/.docker-orkes/stacks/internal-registry/`.

---

## 2. Langkah 1: Buat Template Stack

1. Buka dasbor Docker Orkes (`http://localhost:8080`).
2. Masuk ke tab **Stack Templates**.
3. Klik tombol **+ Add Stack** dan isi formulir:
   - **Slug Name**: `internal-registry`
   - **Catatan / Notes**: `Lightweight Zot OCI Registry with on-demand Docker Hub pull-through cache (ADR 0007)`.
4. Pada kotak **Compose Configuration (`docker-compose.yml`)**, masukkan konfigurasi berikut:

```yaml
services:
  zot:
    image: ghcr.io/project-zot/zot-linux-amd64:latest
    container_name: zot-registry
    restart: always
    ports:
      - "${REGISTRY_PORT:-5000}:5000"
    volumes:
      - ./data:/var/lib/registry
      - ./config.json:/etc/zot/config.json:ro
    environment:
      - ZOT_LOG_LEVEL=info
```

5. Pada kotak **Default Environment Variables (`.env`)**, masukkan:
```env
REGISTRY_PORT=5000
```
6. Klik **Save Stack Template**.

---

## 3. Langkah 2: Deploy ke Server Node Infrastruktur

1. Masuk ke tab **Active Deployments**.
2. Klik tombol **+ New Deployment**.
3. Tentukan opsi deployment:
   - **Pilih Server Target**: Pilih server yang ditunjuk sebagai node infrastruktur/storage (misal `node-infra` atau `node1`).
   - **Pilih Template Stack**: `internal-registry`.
4. Klik **Jalankan Deployment**.

---

## 4. Langkah 3: Konfigurasi Zot via Runtime Assets

Zot membutuhkan file konfigurasi (`config.json`) untuk mengaktifkan fitur *pull-through cache*. Anda dapat membuat dan menyunting file ini langsung melalui antarmuka web Docker Orkes:

1. Pada tab **Active Deployments**, cari baris deployment `internal-registry` lalu klik tombol **📁 Files**.
2. Klik tombol **+📄 File** pada toolbar.
3. Beri nama file: `config.json`.
4. Tempelkan (*paste*) konfigurasi berikut ke dalam editor kode:

```json
{
  "distSpecVersion": "1.1.0-dev",
  "storage": {
    "rootDirectory": "/var/lib/registry",
    "gc": true,
    "gcDelay": "1h",
    "gcInterval": "24h"
  },
  "http": {
    "address": "0.0.0.0",
    "port": "5000"
  },
  "log": {
    "level": "info"
  },
  "extensions": {
    "sync": {
      "enable": true,
      "registries": [
        {
          "urls": [
            "https://registry-1.docker.io"
          ],
          "onDemand": true,
          "tlsVerify": true,
          "maxDownloadRate": 0,
          "content": [
            {
              "prefix": "docker.io/*"
            }
          ]
        }
      ]
    }
  }
}
```

> [!TIP]
> Jika Anda memiliki akun berbayar Docker Hub atau ingin limit pull yang lebih tinggi saat Zot melakukan sinkronisasi ke Docker Hub, Anda dapat menambahkan kredensial akun pada bagian registri:
> ```json
> "credentials": {
>   "username": "username-dockerhub-anda",
>   "password": "pat-atau-token-dockerhub"
> }
> ```

5. Klik tombol **🚀 Save & Redeploy**.
   Docker Orkes akan menyimpan berkas `config.json` via SFTP dan otomatis mengeksekusi `docker compose up -d` untuk memuat konfigurasi baru.

---

## 5. Langkah 4: Konfigurasi Server Node Worker

Agar seluruh node pekerja (*worker nodes*) secara transparan memanfaatkan cache image tanpa perlu mengubah berkas compose aplikasi, konfigurasikan Docker daemon di setiap node:

Anda dapat mengeksekusi langkah ini langsung menggunakan fitur **Web Terminal Host** di dasbor Docker Orkes:

1. Pada tab **Managed Nodes**, klik tombol **Terminal** pada server yang ingin dikonfigurasi.
2. Sunting berkas `/etc/docker/daemon.json` (sesuaikan `192.168.1.50` dengan IP server registry Anda):

```bash
sudo mkdir -p /etc/docker
cat << 'EOF' | sudo tee /etc/docker/daemon.json
{
  "registry-mirrors": ["http://192.168.1.50:5000"],
  "insecure-registries": ["192.168.1.50:5000"]
}
EOF
```

3. Muat ulang dan restart layanan Docker:
```bash
sudo systemctl daemon-reload
sudo systemctl restart docker
```

4. Pastikan Docker telah mendeteksi mirror tersebut:
```bash
docker info | grep -A 3 "Registry Mirrors"
```
Hasil yang diharapkan:
```
 Registry Mirrors:
  http://192.168.1.50:5000/
```

---

## 6. Pengujian & Skenario Penggunaan

### Skenario A: Caching Otomatis On-Demand
Dari salah satu server node yang telah dikonfigurasi, jalankan pull image publik:
```bash
docker pull alpine:latest
```
Daemon Docker akan meminta image ke `http://192.168.1.50:5000`. Zot akan mengunduhnya dari Docker Hub, menyimpannya di disk lokal (`/var/lib/registry`), dan mengirimkannya ke node.

Saat server node kedua menarik image yang sama, image tersebut akan langsung disajikan dari server Zot lokal dalam hitungan milidetik tanpa menggunakan kuota internet Docker Hub!

### Skenario B: Push Image Aplikasi Privat (CI/CD Dual-Push)
Untuk menyimpan image aplikasi internal:

1. **Build dan Push ke Zot**:
   ```bash
   docker build -t 192.168.1.50:5000/tim-kami/aplikasi-web:v1.0.0 .
   docker push 192.168.1.50:5000/tim-kami/aplikasi-web:v1.0.0
   ```

2. **Dual-Push ke Backup Eksternal (Sesuai ADR 0007)**:
   Sebagai cadangan disaster recovery, pipeline CI/CD juga mendorong image yang sama ke registry eksternal (misal Docker Hub atau GitHub Container Registry):
   ```bash
   docker tag 192.168.1.50:5000/tim-kami/aplikasi-web:v1.0.0 organisasi/aplikasi-web:v1.0.0
   docker push organisasi/aplikasi-web:v1.0.0
   ```

3. **Gunakan di Stack Docker Orkes**:
   Pada definisi `docker-compose.yml` template stack Anda:
   ```yaml
   services:
     web:
       image: 192.168.1.50:5000/tim-kami/aplikasi-web:v1.0.0
       ports:
         - "80:80"
   ```

---

## 7. Penanganan Bencana & Mekanisme Failover

- **Jika Server Registry Offline**:
  - Node worker akan mencoba menghubungi `192.168.1.50:5000`. Jika tidak dapat diakses, daemon Docker secara otomatis (*fail-open*) menarik image publik langsung dari Docker Hub resmi.
  - Untuk image aplikasi privat, ubah referensi image pada template stack ke repositori cadangan (misal `organisasi/aplikasi-web:v1.0.0`) lalu klik **Save & Redeploy**.
- **Ketahanan Data**:
  - Image kontainer tersimpan di server registry pada folder `~/.docker-orkes/stacks/internal-registry/data/`. Pencadangan berkala dapat dilakukan dengan rsync atau snapshot volume disk.
