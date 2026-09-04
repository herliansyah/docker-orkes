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

---

## Fitur Utama

- 🚀 **Arsitektur Agentless SSH**: Tanpa jejak software di server remote. Selama server tujuan dapat menjalankan Docker dan menerima koneksi SSH, server tersebut siap dikelola oleh Docker Orkes.
- 📦 **Template Stack Deklaratif**: Definisikan template aplikasi `docker-compose.yml` yang dapat digunakan ulang ke berbagai server node dengan variabel lingkungan yang dapat disesuaikan.
- 🔍 **Deteksi Configuration Drift Real-Time**: Verifikasi checksum SHA-256 dan tinjau diff baris-demi-baris antara *Desired State* di Control Plane dan *Actual State* di server remote. Anda dapat memilih untuk **Reconcile** (menimpa server remote) atau **Adopt** (mengadopsi perubahan remote ke Git/Control Plane).
- 💻 **100% Offline Interactive Web Terminal**: Buka sesi terminal PTY langsung melalui browser via WebSocket ke host server maupun ke dalam container (`docker exec`) menggunakan aset xterm.js offline mandiri tanpa dependensi internet.
- 📜 **Deployment Revisions & 1-Click Rollback**: Riwayat snapshot konfigurasi dan environment variables *forward-only* yang *immutable* dengan retensi terkelola untuk rollback instan dalam satu klik.
- 📂 **Remote Stack Filesystem & Runtime Assets**: Navigasi struktur folder remote, pembuatan file/folder baru, upload berkas secara drag-and-drop dan folder rekursif (`webkitdirectory`), ekstraksi arsip remote (`.tar.gz`, `.zip`), proteksi masking rahasia, serta live editor dengan redeploy instan.
- 🔔 **Proactive Liveness Poller & Alerting**: Pemeriksaan latar belakang otomatis terhadap status node, kesehatan kontainer, dan drift konfigurasi dengan notifikasi langsung via Telegram Bot, Discord Webhook, atau HTTP Webhook.
- 📊 **Observabilitas Host & Log Kontainer**: Pantau kapasitas penyimpanan disk (`df -h`), vital Docker engine, pemetaan port kontainer, dan peninjau live logs kontainer secara real-time.
- 🔄 **Backup Otomatis ke Git Mirror**: Sinkronkan seluruh konfigurasi stack, env node, dan `inventory.json` ke repositori Git eksternal (GitHub, GitLab, Gitea) sebagai audit log berversi.
- ⚡ **Auto-Inject SSH Key**: Pasang Control Plane SSH Public Key ke server target baru secara otomatis menggunakan password sementara sekali pakai (password tidak pernah disimpan ke database).
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
go build -o docker-orkes ./cmd/docker-orkes
```

### 2. Jalankan Control Plane
```bash
./docker-orkes -addr :8080 -pass password-rahasia-anda
```

Buka peramban (browser) Anda dan akses `http://localhost:8080`. Masuk menggunakan password yang telah Anda tetapkan.

### Parameter Opsi & Environment Variables

| Flag CLI | Environment Variable | Nilai Default | Keterangan |
| :--- | :--- | :--- | :--- |
| `-addr` | `ADDR` | `:8080` | Alamat listen dan port web server |
| `-pass` | `MASTER_PASSWORD` | `admin` | Password login master untuk dasbor |
| `-db` | `DB_PATH` | `docker-orkes.db` | Jalur file database SQLite |

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
