# Feature Deep Dive & Architecture

This document provides a comprehensive technical overview of Docker Orkes' design, operational model, and key capabilities.

---

## 1. Core Principles

- **Agentless Simplicity**: No daemons, runners, or agent software installed on managed hosts. Everything is driven through standard SSH connections using Go's pure cryptographic libraries (`golang.org/x/crypto/ssh`).
- **Declarative Stacks**: Applications are defined as standard, reusable Docker Compose templates.
- **Single-Binary Footprint**: Control Plane runs as a standalone Go binary with embedded assets (`embed.FS`) and an embedded SQLite database.
- **Auditability via Git Mirror**: Every change, deployment configuration, and inventory change is committed to a versioned Git audit log.

---

## 2. Remote Stack File Structure

On remote nodes, Docker Orkes organizes deployed applications inside a clean, deterministic path structure in the user's home directory:

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

## 3. Configuration Drift Detection and Reconciliation

### What is Drift?
Drift occurs when the configuration on the remote host (*Actual State*) diverges from the configuration stored in the Control Plane (*Desired State*). This can happen if an operator SSHs directly into the server and manually modifies `docker-compose.yml` or `.env`.

### How Drift Detection Works:
1. When you trigger **Check Drift**, Docker Orkes connects to the remote host via SFTP.
2. It fetches the remote `docker-compose.yml` and `.env` files.
3. It generates unified diffs between the local Desired State and the remote Actual State.
4. If discrepancies exist, the deployment is marked as **⚠️ Drifted**, and a detailed line-by-line diff is displayed.

### Drift Resolution Actions:
- **Reconcile (Overwrite Remote)**: The Control Plane re-uploads the Desired State from the database and runs `docker compose up -d` to enforce compliance.
- **Adopt Remote**: The Control Plane ingests the remote server's changes as the new Desired State and commits the updated configuration to the Git Mirror.

---

## 4. Live Remote SFTP File Editor

Each active deployment includes an integrated file browser and editor:
- **Browse Files**: View all files and directories within `~/.docker-orkes/stacks/<stack-name>/`.
- **Live Code Editor**: View syntax, make immediate edits to `docker-compose.yml` or `.env`.
- **Save & Redeploy**: Save the file directly to the remote node over SFTP and trigger an instant `docker compose up -d` redeploy in a single click.

---

## 5. Host Observability & Telemetry

When viewing a node's detail page (`/nodes/<id>`), Docker Orkes samples telemetry non-intrusively over SSH:
- **Disk Space**: Queries `df -h /` to parse total capacity, consumed gigabytes, free space, and percentage utilization.
- **Docker Engine Vitals**: Inspects `docker info` and queries `docker ps -a --format json` to report container status, health, image tags, and exposed port mappings.

---

## 6. Git Mirror Backup Architecture

Docker Orkes acts as a Git client using `go-git`:
1. It maintains a local repository mirror on the Control Plane under `data/git-mirror/`.
2. On each change (create stack, update stack, create deployment, update env vars, or edit notes), Docker Orkes updates the local tree:
   ```
   git-mirror/
   ├── inventory.json
   ├── RUNBOOK.md
   ├── stacks/
   │   └── <stack-name>/docker-compose.yml
   └── deployments/
       └── <node-name>/<stack-name>.env
   ```
3. When sync is triggered, it creates a signed commit and pushes the branch to the remote repository (GitHub, GitLab, etc.).
