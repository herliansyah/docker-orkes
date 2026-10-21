<h1 align="center">🐳 Docker Orkes</h1>

<p align="center">
  <strong>Control plane multi-node Docker deployment dan observabilitas host berbasis template deklaratif Docker Compose tanpa agen (agentless).</strong>
</p>

<p align="center">
  <a href="#fitur-utama">Fitur Utama</a> •
  <a href="#arsitektur">Arsitektur</a> •
  <a href="#panduan-cepat">Panduan Cepat</a> •
  <a href="#dokumentasi">Dokumentasi</a> •
  <a href="#tech-stack">Tech Stack</a> •
  <a href="README.md">English Version 🇬🇧</a>
</p>

---

## Gambaran Umum

**Docker Orkes** menjembatani kesenjangan operasional antara pengelolaan `docker compose` di satu server dan cluster orchestrator yang berat seperti Kubernetes atau Docker Swarm. Docker Orkes memungkinkan sysadmin dan DevOps engineer mengelola puluhan VPS atau server bare-metal independen dari satu antarmuka Control Plane terpusat tanpa perlu memasang agen, runner, maupun daemon latar belakang apa pun di server tujuan.

Semua operasi dijalankan melalui protokol aman **SSH (Agentless)**:
- Distribusi template deklaratif Docker Compose.
- Injeksi variabel lingkungan node yang terisolasi (`.env`).
- Pemantauan real-time kapasitas disk fisik host dan status container Docker.
- Pengeditan langsung file stack remote dengan editor kode SFTP terintegrasi.
- Deteksi otomatis perbedaan konfigurasi (*drift*) antara status yang diinginkan (*Desired State*) dan kondisi aktual di server remote (*Actual State*).
- Pencadangan otomatis semua manifest stack dan penugasan node ke repositori **Git Mirror**.

### Filosofi Desain: Pragmatis, Ramping, dan Mandiri

- **Agentless Tanpa Daemon**: Tanpa agen atau daemon kluster (Swarm/Kubernetes) yang memakan RAM atau butuh perawatan berkala di server remote. Cukup SSH standar dan Docker Engine.
- **Deklaratif Bukan Imperatif**: Kelola aplikasi sebagai template Compose dan deployment yang dapat direproduksi dengan deteksi drift otomatis, bukan manipulasi kontainer ad-hoc.
- **Pemindaian Kerentanan Agentless Tanpa Bloat di Runtime**: Docker Orkes mengaudit keamanan image kontainer dengan menjalankan kontainer scanner Trivy secara ephemeral di server node target via SSH Executor, mencatat metrik CVE ke SQLite tanpa membebani binary Control Plane dengan database CVE yang besar.
- **100% Mandiri (Self-Contained)**: Single static binary Go tanpa dependensi CDN eksternal, SQLite tertanam, dan web terminal offline.

---

## Fitur Utama

- 🚀 **Arsitektur Agentless SSH**: Tanpa jejak software di server remote. Selama server tujuan dapat menjalankan Docker dan menerima koneksi SSH, server tersebut siap dikelola oleh Docker Orkes.
- 📦 **Template Stack Deklaratif**: Definisikan template aplikasi `docker-compose.yml` yang dapat digunakan ulang ke berbagai server node dengan variabel lingkungan yang dapat disesuaikan.
- 🔍 **Deteksi Configuration Drift Real-Time**: Verifikasi checksum SHA-256 dan tinjau diff baris-demi-baris antara *Desired State* di Control Plane dan *Actual State* di server remote. Anda dapat memilih untuk **Reconcile** (menimpa server remote) atau **Adopt** (mengadopsi perubahan remote ke Git/Control Plane).
- 🛡️ **Pemindaian Kerentanan Image Agentless & Protokol Cooldown**: Audit CVE image kontainer pada Node menggunakan kontainer transient Trivy dengan cache host terdedikasi (`orkes-trivy-cache`), proteksi scan cooldown 24 jam untuk mencegah kehabisan resource/rate-limiting, pemantauan progress bulk scan di background secara real-time, dan modal interaktif laporan kerentanan berisikan versi paket terdampak serta ID CVE.
- 💻 **100% Offline Interactive Web Terminal**: Buka sesi terminal PTY langsung melalui browser via WebSocket ke host server maupun ke dalam container (`docker exec`) menggunakan aset xterm.js offline mandiri tanpa dependensi internet.
- 📜 **Deployment Revisions & 1-Click Rollback**: Riwayat snapshot konfigurasi dan environment variables *forward-only* yang *immutable* dengan retensi terkelola untuk rollback instan dalam satu klik.
- 📂 **Remote Stack Filesystem & Runtime Assets**: Navigasi struktur folder remote, pembuatan file/folder baru, upload berkas secara drag-and-drop dan folder rekursif (`webkitdirectory`), ekstraksi arsip remote (`.tar.gz`, `.zip`), proteksi masking rahasia, serta live editor dengan redeploy instan.
- 🔔 **Proactive Liveness Poller & Alerting**: Pemeriksaan latar belakang otomatis terhadap status node, kesehatan kontainer, dan drift konfigurasi dengan notifikasi langsung via Telegram Bot, Discord Webhook, atau HTTP Webhook.
- 📊 **Observabilitas Host & Log Kontainer**: Pantau kapasitas penyimpanan disk (`df -h`), vital Docker engine, pemetaan port kontainer, dan peninjau live logs kontainer secara real-time.
- 🔄 **Backup Otomatis ke Git Mirror**: Sinkronkan seluruh konfigurasi stack, env node, dan `inventory.json` ke repositori Git eksternal (GitHub, GitLab, Gitea) sebagai audit log berversi.
- ⚡ **Auto-Inject SSH Key**: Pasang Control Plane SSH Public Key ke server target baru secara otomatis menggunakan password sementara sekali pakai (password tidak pernah disimpan ke database).
- 📡 **In-App Activity Console & Live Telemetry Stream**: Saluran telemetry diagnostik real-time berbasis Server-Sent Events (SSE) dengan drawer 6 kolom (`TIME | LEVEL | MODULE | TARGET/NODE | MESSAGE | DETAILS`), smart auto-scroll, kontrol pause/resume, filter severity, counter error baru, shortcut global (`Ctrl+```), serta export instan `.log` dan `.json`.
- 🌐 **Antarmuka Bilingual**: Beralih antara Bahasa Indonesia dan Bahasa Inggris secara instan langsung dari antarmuka dashboard.
- 📋 **Operator Runbook Bersama**: Papan catatan checklist operasional dan jadwal maintenance yang tersinkronisasi otomatis ke Git sebagai file `RUNBOOK.md`.

---

## Arsitektur

```
                 ┌─────────────────────────────────────────┐
                 │       Docker Orkes Control Plane        │
                 │  (Go Single Binary + SQLite + embed.FS) │
                 └───────────────────┬─────────────────────┘
                                     │
         ┌───────────────────────────┼───────────────────────────┐
         │ SSH (Agentless)           │ SSH (Agentless)           │ Git SSH/HTTPS
         ▼                           ▼                           ▼
┌─────────────────┐         ┌─────────────────┐         ┌─────────────────┐
│     Node 1      │         │     Node 2      │         │   Git Mirror    │
│  (Docker Host)  │         │  (Docker Host)  │         │(GitHub / GitLab)│
│                 │         │                 │         │                 │
│ ~/.docker-orkes │         │ ~/.docker-orkes │         │ - Stacks        │
│  /stacks/...    │         │  /stacks/...    │         │ - Deployments   │
└─────────────────┘         └─────────────────┘         │ - inventory.json│
                                                        │ - RUNBOOK.md    │
                                                        └─────────────────┘
```

---

## Panduan Cepat

### Prasyarat
- **Host Control Plane**: Linux atau macOS dengan Go 1.22+ (atau binary siap pakai).
- **Server Target (Node)**: Server Linux dengan Docker dan SSH server aktif (`systemctl status docker`).

### 1. Kompilasi Binary
```bash
git clone https://github.com/herliansyah/docker-orkes.git
cd docker-orkes
go build -o bin/docker-orkes ./cmd/docker-orkes
```

### 2. Jalankan Control Plane
```bash
./bin/docker-orkes -addr :8080 -pass password-rahasia-anda
```

Buka peramban (browser) Anda dan akses `http://localhost:8080`. Masuk menggunakan password yang telah Anda tetapkan.

### Parameter Opsi & Environment Variables

| Flag CLI | Environment Variable | Nilai Default | Keterangan |
| :--- | :--- | :--- | :--- |
| `-addr` | `ADDR` | `:8080` | Alamat listen dan port web server |
| `-pass` | `MASTER_PASSWORD` | `admin` | Password login master untuk dasbor |
| `-db` | `DB_PATH` | `docker-orkes.db` | Jalur file database SQLite |
| `-log-file` | `LOG_FILE` | `docker-orkes.log` | Jalur file log persisten berotasi di server (kosongkan untuk menonaktifkan) |
| `-log-level` | `LOG_LEVEL` | `info` | Level logging diagnostik (`debug`, `info`, `warn`, `error`) |

---

## ⚠️ Arsitektur Penyimpanan & Praktik Terbaik Persistensi Data

> [!CAUTION]
> **PANTANG MENYIMPAN DATA PERSISTEN ATAU DATABASE DI DALAM `~/.docker-orkes/`**
>
> Stack Directory (`~/.docker-orkes/stacks/<stack-name>/`) dialokasikan secara ketat hanya untuk **file konfigurasi deklaratif** (`docker-compose.yml`, `.env`) dan aset konfigurasi ringan (seperti file config Nginx atau sertifikat SSL).
> 
> Saat operator menjalankan **Deployment Deletion dengan opsi Purge Data**, Docker Orkes akan mengeksekusi `docker compose down -v` dilanjutkan dengan perintah pemusnahan total `rm -rf` terhadap Stack Directory. Jika Anda menaruh folder database (misal bind mount `./data:/var/lib/mysql`) di dalam folder stack ini, **DATABASE AKAN HILANG PERMANEN SECARA TOTAL**.
>
> **Fitur Pengaman Otomatis (Safety Guardrail)**: Untuk mencegah malapetaka terhapusnya database yang salah taruh, Docker Orkes secara otomatis mengaudit ukuran folder Stack Directory sebelum menjalankan `rm -rf`. Jika ukuran folder melebihi **50 MB**, proses purge akan **DIBLOKIR OTOMATIS** oleh Control Plane.

### Pola yang Benar untuk Partisi Khusus (misal `/mydocker`) & Data Persisten

Jika server Node Anda memiliki partisi disk terpisah (seperti `/mydocker` atau `/data`) khusus untuk Docker:

1. **Penyimpanan Database / Data Aplikasi Persisten**:
   Selalu gunakan **absolute path di luar `~/.docker-orkes/`** atau gunakan **Docker Named Volumes**:
   ```yaml
   services:
     db:
       image: mysql:8.0
       volumes:
         # ✅ BENAR: Absolute host path di partisi khusus Anda
         - /mydocker/mysql-data:/var/lib/mysql
         # ATAU
         # ✅ BENAR: Docker Named Volume (disimpan di data-root daemon Docker)
         - mysql_data:/var/lib/mysql
   ```
2. **Memindahkan Seluruh `.docker-orkes` ke Partisi Khusus**:
   Jangan mengubah kode aplikasi. Cukup gunakan symbolic link native OS di server Node:
   ```bash
   mkdir -p /mydocker/docker-orkes
   ln -s /mydocker/docker-orkes ~/.docker-orkes
   ```
   Folder stack akan tersimpan di partisi Anda secara transparan dengan keamanan dan isolasi yang tetap terjaga penuh.

---

## Dokumentasi Lengkap

Panduan langkah demi langkah dan rincian teknis telah tersedia:

- 📖 [Panduan Memulai (Bahasa Indonesia)](docs/getting-started.id.md) | [Getting Started Guide (English)](docs/getting-started.md)
- 🛠️ [Rincian Fitur & Arsitektur (Bahasa Indonesia)](docs/features.id.md) | [Feature Deep Dive & Architecture (English)](docs/features.md)
- 📦 [Panduan Setup Registry Internal (Bahasa Indonesia)](docs/internal-registry.id.md) | [Internal Hybrid Image Registry Setup (English)](docs/internal-registry.md)
- 🛡️ [Panduan Disaster Recovery & Standby (Bahasa Indonesia)](docs/disaster-recovery.id.md) | [Disaster Recovery & Standby Setup (English)](docs/disaster-recovery.md)
- 📐 [Architecture Decision Records (ADRs)](docs/adr/)
  - [ADR 0001: Agentless SSH Architecture](docs/adr/0001-agentless-ssh-architecture.md)
  - [ADR 0002: Control Plane Tech Stack](docs/adr/0002-control-plane-tech-stack.md)
  - [ADR 0003: Stack Template and Deployment Model](docs/adr/0003-stack-template-and-deployment-model.md)
  - [ADR 0004: Lightweight Liveness Poller and Edge-Triggered Alerting](docs/adr/0004-lightweight-poller-and-edge-triggered-alerting.md)
  - [ADR 0005: Forward-Only Deployment Revisions with Bounded Retention](docs/adr/0005-forward-only-deployment-revisions-with-bounded-retention.md)
  - [ADR 0006: Active-Passive Control Plane Replication](docs/adr/0006-active-passive-control-plane-replication.md)
  - [ADR 0007: Internal Hybrid Image Registry Architecture](docs/adr/0007-internal-hybrid-image-registry.md)
  - [ADR 0008: Remote Stack Filesystem and Runtime Assets Management](docs/adr/0008-remote-stack-filesystem-and-runtime-assets.md)
  - [ADR 0009: Offline Web Terminal Architecture via WebSocket and PTY](docs/adr/0009-offline-web-terminal-pty.md)
  - [ADR 0010: Node Images and On-Demand Retention](docs/adr/0010-node-images-and-ondemand-retention.md)
  - [ADR 0011: Self-Contained Enterprise UI](docs/adr/0011-self-contained-enterprise-ui.md)
  - [ADR 0012: Node Volumes and Networks Observability](docs/adr/0012-node-volumes-and-networks-observability.md)
  - [ADR 0013: Deployment Deletion Lifecycle and Adoption Safety](docs/adr/0013-deployment-deletion-lifecycle-and-adoption-safety.md)
  - [ADR 0014: Deployment Preflight Checks and Execution Feedback](docs/adr/0014-deployment-preflight-checks-and-execution-feedback.md)
  - [ADR 0015: Agentless Transport Security and Filesystem Isolation](docs/adr/0015-agentless-transport-security-and-filesystem-isolation.md)
  - [ADR 0016: Stack Directory Isolation and Data Purge Guardrails](docs/adr/0016-stack-directory-isolation-and-data-purge-guardrails.md)
  - [ADR 0017: In-App Activity Console and Live Telemetry Stream](docs/adr/0017-in-app-activity-console-and-live-telemetry-stream.md)

---

## Tech Stack

- **Backend**: Go (Standard Library + `net/http` native routing)
- **Database**: SQLite dengan pure-Go driver Modernc (`modernc.org/sqlite`)
- **Transport**: `golang.org/x/crypto/ssh` & `github.com/pkg/sftp`
- **Frontend**: HTML5 Templates di-embed saat kompilasi (`embed.FS`) menggunakan Tailwind CSS
- **Version Control**: Pure-Go Git implementation (`github.com/go-git/go-git/v5`)

---

## Kontribusi

Kontribusi kode, ide fitur, dan laporan bug sangat dipersilakan! Silakan buat issue atau pull request di [halaman issues](https://github.com/herliansyah/docker-orkes/issues).

---

## Lisensi

Didistribusikan di bawah **Lisensi MIT**. Lihat [LICENSE](LICENSE) untuk rincian selengkapnya.

Dibuat dengan dedikasi oleh **[Herliansyah](https://github.com/herliansyah)**.
