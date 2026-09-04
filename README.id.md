# Docker Orkes

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
- 📂 **Live Remote SFTP Editor**: Jelajahi direktori stack remote (`~/.docker-orkes/stacks/<stack-name>/`), buka file, edit langsung, dan jalankan redeploy instan `docker compose up -d` langsung dari web browser.
- 📊 **Observabilitas Host**: Pantau kapasitas penyimpanan root disk (`df -h`) dan ringkasan vital container Docker beserta pemetaan port jaringan secara langsung.
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
./docker-orkes -addr :9000 -pass password-rahasia-anda
```

Parameter yang didukung:
- `-addr`: Alamat listen web server (default: `:9000`).
- `-pass`: Password login master untuk dasbor control plane.
- `-db`: Jalur file database SQLite (default: `docker-orkes.db`).

Buka peramban (browser) Anda dan akses `http://localhost:9000`. Masuk menggunakan password yang telah Anda tetapkan.

---

## Dokumentasi Lengkap

Panduan langkah demi langkah dan rincian teknis telah tersedia:

- 📖 [Panduan Memulai (Bahasa Indonesia)](docs/getting-started.id.md) | [Getting Started Guide (English)](docs/getting-started.md)
- 🛠️ [Rincian Fitur & Arsitektur (Bahasa Indonesia)](docs/features.id.md) | [Feature Deep Dive & Architecture (English)](docs/features.md)
- 🛡️ [Panduan Disaster Recovery & Standby (Bahasa Indonesia)](docs/disaster-recovery.id.md) | [Disaster Recovery & Standby Setup (English)](docs/disaster-recovery.md)
- 📐 [Architecture Decision Records (ADRs)](docs/adr/)
  - [ADR 0001: Agentless SSH Architecture](docs/adr/0001-agentless-ssh-architecture.md)
  - [ADR 0002: Control Plane Tech Stack](docs/adr/0002-control-plane-tech-stack.md)
  - [ADR 0003: Stack Template and Deployment Model](docs/adr/0003-stack-template-and-deployment-model.md)
  - [ADR 0004: Lightweight Liveness Poller and Edge-Triggered Alerting](docs/adr/0004-lightweight-poller-and-edge-triggered-alerting.md)
  - [ADR 0005: Forward-Only Deployment Revisions with Bounded Retention](docs/adr/0005-forward-only-deployment-revisions-with-bounded-retention.md)
  - [ADR 0006: Active-Passive Control Plane Replication](docs/adr/0006-active-passive-control-plane-replication.md)

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
