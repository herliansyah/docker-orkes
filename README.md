# Docker Orkes

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

---

## Key Features

- 🚀 **Agentless SSH Architecture**: Zero software footprint on target nodes. If the target server can run Docker and accept SSH connections, it can be managed by Docker Orkes.
- 📦 **Declarative Stack Templates**: Define reusable `docker-compose.yml` templates and deploy them across any target node with customized environment variables.
- 🔍 **Real-Time Configuration Drift Detection**: Verify SHA-256 checksums and review line-by-line diffs between the Control Plane's Desired State and the Actual State on remote nodes. Choose to either **Reconcile** (overwrite remote) or **Adopt** (pull remote changes into Git).
- 📂 **Live Remote SFTP Editor**: Browse remote stack directories (`~/.docker-orkes/stacks/<stack-name>/`), view files, edit them directly, and trigger instant `docker compose up -d` redeployments without leaving the web UI.
- 📊 **Host Observability**: Live disk usage utilization graphs (`df -h`) and Docker container vitals with real-time status and port bindings.
- 🔄 **Automated Git Mirror Backup**: Maintain an external Git repository (GitHub, GitLab, Gitea) as a versioned audit log of all stack templates, deployment variables, and inventory.
- ⚡ **Auto-Inject SSH Key**: Bootstrap new nodes automatically using temporary SSH passwords—the password is used once to append the Control Plane's public key to `~/.ssh/authorized_keys` and is never saved.
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
go build -o docker-orkes ./cmd/docker-orkes
```

### 2. Launch Control Plane
```bash
./docker-orkes -addr :9000 -pass your-secure-password
```

Parameters:
- `-addr`: Web server listening address (default: `:9000`).
- `-pass`: Master login password for the control plane dashboard.
- `-db`: SQLite database file path (default: `docker-orkes.db`).

Open your browser and navigate to `http://localhost:9000`. Log in with your password.

---

## Documentation

Comprehensive guides and technical documentation are available:

- 📖 [Getting Started Guide (English)](docs/getting-started.md) | [Panduan Memulai (Bahasa Indonesia)](docs/getting-started.id.md)
- 🛠️ [Feature Deep Dive & Architecture (English)](docs/features.md) | [Rincian Fitur & Arsitektur (Bahasa Indonesia)](docs/features.id.md)
- 📐 [Architecture Decision Records (ADRs)](docs/adr/)
  - [ADR 0001: Agentless SSH Architecture](docs/adr/0001-agentless-ssh-architecture.md)
  - [ADR 0002: Control Plane Tech Stack](docs/adr/0002-control-plane-tech-stack.md)
  - [ADR 0003: Stack Template and Deployment Model](docs/adr/0003-stack-template-and-deployment-model.md)

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
