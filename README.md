<h1 align="center">🐳 Docker Orkes</h1>

<p align="center">
  <strong>A lightweight, agentless multi-node Docker deployment and host observability control plane based on declarative Docker Compose stacks.</strong>
</p>

<p align="center">
  <a href="#key-features">Features</a> •
  <a href="#architecture">Architecture</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#documentation">Documentation</a> •
  <a href="#tech-stack">Tech Stack</a> •
  <a href="README.id.md">Bahasa Indonesia 🇮🇩</a>
</p>

---

## Overview

**Docker Orkes** solves the operational gap between single-server `docker compose` and heavy cluster orchestrators like Kubernetes or Docker Swarm. It allows DevOps engineers and sysadmins to manage dozens of independent VPS or bare-metal servers through a single unified Control Plane without installing any agents, runners, or background daemons on the target hosts.

Everything is performed over secure, agentless **SSH**:
- Push declarative Docker Compose templates.
- Inject node-specific environment variables (`.env`).
- Monitor remote disk capacity and container runtimes in real time.
- Edit remote stack files live with an integrated SFTP code editor.
- Automatically detect configuration drift between desired state and actual remote state.
- Backup all stacks and deployment manifests to an external **Git Mirror**.

### Design Philosophy: Pragmatic, Lean, and Self-Contained

- **Agentless over Daemons**: No agents or cluster daemons (Swarm/Kubernetes) consuming RAM or requiring maintenance on target nodes. Plain SSH and Docker are all you need.
- **Declarative over Imperative**: Manage applications as reproducible Compose templates and deployments with automatic drift detection, rather than ad-hoc container clicking.
- **Security in CI/CD, Not Runtime Bloat**: Docker Orkes deliberately does not bundle heavy container vulnerability scanners (like Trivy or external CVE feeds). Vulnerability scanning belongs in your CI/CD build pipeline or secure container registry, keeping your production deployment control plane lean, fast, and 100% offline-capable.
- **100% Self-Contained**: Single static Go binary with zero external CDN dependencies, embedded SQLite, and embedded web terminal.

---

## Key Features

- 🚀 **Agentless SSH Architecture**: Zero software footprint on target nodes. If the target server can run Docker and accept SSH connections, it can be managed by Docker Orkes.
- 📦 **Declarative Stack Templates**: Define reusable `docker-compose.yml` templates and deploy them across any target node with customized environment variables.
- 🔍 **Real-Time Configuration Drift Detection**: Verify SHA-256 checksums and review line-by-line diffs between the Control Plane's Desired State and the Actual State on remote nodes. Choose to either **Reconcile** (overwrite remote) or **Adopt** (pull remote changes into Git).
- 💻 **100% Offline Interactive Web Terminal**: Launch low-latency browser PTY terminal sessions over WebSocket directly into node host shells or running container environments (`docker exec`) with bundled offline xterm.js assets.
- 📜 **Deployment Revisions & 1-Click Rollback**: Maintain forward-only, immutable snapshot histories of compose configurations and environment variables with bounded retention for instant rollbacks.
- 📂 **Remote Stack Filesystem & Runtime Assets**: Full remote directory browser, file creation, drag-and-drop & recursive folder upload (`webkitdirectory`), remote archive extraction (`.tar.gz`, `.zip`), secret masking, and live code editing with instant redeployment.
- 🔔 **Proactive Liveness Poller & Alerting**: Automatic background auditing of host reachability, container health, and drift with edge-triggered webhook dispatches to Telegram, Discord, or generic endpoints.
- 📊 **Host Observability & Container Logs**: Real-time disk usage telemetry (`df -h`), Docker engine vitals, container port mappings, and live container logs viewer.
- 🔄 **Automated Git Mirror Backup**: Maintain an external Git repository (GitHub, GitLab, Gitea) as a versioned audit log of all stack templates, deployment variables, and inventory.
- ⚡ **Auto-Inject SSH Key**: Bootstrap new nodes automatically using temporary SSH passwords—the password is used once to append the Control Plane's public key to `~/.ssh/authorized_keys` and is never saved.
- 📡 **In-App Activity Console & Live Telemetry Stream**: Real-time zero-polling diagnostic feed served via Server-Sent Events (SSE) with a 6-column running drawer (`TIME | LEVEL | MODULE | TARGET/NODE | MESSAGE | DETAILS`), smart auto-scroll, pause/resume, level filters, unread error counters, global hotkey (`Ctrl+```), and client-side `.log` and `.json` export.
- 🌐 **Bilingual Interface**: Seamlessly switch between English and Bahasa Indonesia with instant client-side localization.
- 📋 **Shared Operator Runbook**: Built-in operational checklist and infrastructure notes synchronized directly to your Git repository as `RUNBOOK.md`.

---

## Architecture

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

## Quick Start

### Prerequisites
- **Control Plane host**: Linux or macOS with Go 1.22+ (or pre-compiled binary).
- **Target Nodes**: Any Linux server with Docker and SSH server enabled (`systemctl status docker`).

### 1. Build the Binary
```bash
git clone https://github.com/herliansyah/docker-orkes.git
cd docker-orkes
go build -o bin/docker-orkes ./cmd/docker-orkes
```

### 2. Launch Control Plane
```bash
./bin/docker-orkes -addr :8080 -pass your-secure-password
```

Open your browser and navigate to `http://localhost:8080`. Log in with your password.

### Configuration Flags & Environment Variables

| Flag | Environment Variable | Default | Description |
| :--- | :--- | :--- | :--- |
| `-addr` | `ADDR` | `:8080` | Web server listening address and port |
| `-pass` | `MASTER_PASSWORD` | `admin` | Master login password for the control plane |
| `-db` | `DB_PATH` | `docker-orkes.db` | SQLite database file path |
| `-log-file` | `LOG_FILE` | `docker-orkes.log` | Persistent rotating server log file path (empty to disable) |
| `-log-level` | `LOG_LEVEL` | `info` | Diagnostic logging level (`debug`, `info`, `warn`, `error`) |

---

## ⚠️ Storage Architecture & Data Persistence Best Practices

> [!CAUTION]
> **NEVER STORE PERSISTENT APPLICATION DATA OR DATABASES INSIDE `~/.docker-orkes/`**
>
> The Stack Directory (`~/.docker-orkes/stacks/<stack-name>/`) is reserved strictly for **declarative configuration files** (`docker-compose.yml`, `.env`) and lightweight auxiliary assets (Nginx configs, SSL certificates).
> 
> When an operator performs a **Deployment Deletion with Data Purge**, Docker Orkes executes `docker compose down -v` followed by an automated `rm -rf` on the Stack Directory. If persistent databases (e.g. `./data:/var/lib/mysql`) are placed inside this directory, **THEY WILL BE PERMANENTLY DESTROYED**.
>
> **Built-in Guardrail**: To protect against accidental destruction of misplaced databases, Docker Orkes audits the Stack Directory size before running `rm -rf`. If the directory exceeds **50 MB**, the automated purge is **blocked immediately** by the Control Plane.

### Proper Patterns for Dedicated Partitions & Persistent Data (e.g. `/mydocker`)

If your Node has a dedicated disk partition (e.g. `/mydocker` or `/data`) intended for Docker data:

1. **Persistent Databases / App Storage**:
   Always mount volumes using **absolute host paths outside `~/.docker-orkes/`** or **Docker Named Volumes**:
   ```yaml
   services:
     db:
       image: mysql:8.0
       volumes:
         # ✅ CORRECT: Absolute host path on your dedicated partition
         - /mydocker/mysql-data:/var/lib/mysql
         # OR
         # ✅ CORRECT: Docker Named Volume (stored under Docker daemon's data-root)
         - mysql_data:/var/lib/mysql
   ```
2. **Relocating Entire `.docker-orkes` to a Dedicated Partition**:
   Do not modify application code. Use a native OS symbolic link on the Node:
   ```bash
   mkdir -p /mydocker/docker-orkes
   ln -s /mydocker/docker-orkes ~/.docker-orkes
   ```
   This offloads stack files transparently to your partition while maintaining full compatibility and sandboxing.

---

## Documentation

Comprehensive guides and technical documentation are available:

- 📖 [Getting Started Guide (English)](docs/getting-started.md) | [Panduan Memulai (Bahasa Indonesia)](docs/getting-started.id.md)
- 🛠️ [Feature Deep Dive & Architecture (English)](docs/features.md) | [Rincian Fitur & Arsitektur (Bahasa Indonesia)](docs/features.id.md)
- 📦 [Internal Hybrid Image Registry Setup (English)](docs/internal-registry.md) | [Panduan Setup Registry Internal (Bahasa Indonesia)](docs/internal-registry.id.md)
- 🛡️ [Disaster Recovery & Standby Setup (English)](docs/disaster-recovery.md) | [Disaster Recovery & Standby Setup (Bahasa Indonesia)](docs/disaster-recovery.id.md)
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

- **Backend**: Go (Standard Library + `net/http` routing)
- **Database**: SQLite with Modernc pure-Go driver (`modernc.org/sqlite`)
- **Transport**: `golang.org/x/crypto/ssh` & `github.com/pkg/sftp`
- **Frontend**: HTML5 Templates embedded at compile time (`embed.FS`) styled with Tailwind CSS
- **Version Control**: Pure-Go Git implementation (`github.com/go-git/go-git/v5`)

---

## Contributing

Contributions, feature requests, and bug reports are welcome! Feel free to check the [issues page](https://github.com/herliansyah/docker-orkes/issues).

---

## License

Distributed under the **MIT License**. See [LICENSE](LICENSE) for more information.

Crafted with care by **[Herliansyah](https://github.com/herliansyah)**.
