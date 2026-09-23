# Feature Deep Dive & Architecture

This document provides a comprehensive technical overview of Docker Orkes' design, operational model, and key capabilities.

---

## 1. Core Principles

- **Agentless Simplicity**: No daemons, runners, or agent software installed on managed hosts. Everything is driven through standard SSH connections using Go's pure cryptographic libraries (`golang.org/x/crypto/ssh`).
- **Declarative Stacks**: Applications are defined as standard, reusable Docker Compose templates.
- **Single-Binary Footprint**: Control Plane runs as a standalone Go binary with embedded assets (`embed.FS`) and an embedded SQLite database.
- **Auditability via Git Mirror**: Every change, deployment configuration, and inventory change is committed to a versioned Git audit log.
- **Pragmatic Security Boundaries**: Vulnerability scanning belongs in the CI/CD build pipeline or secure container registry, rather than bloating the deployment control plane with volatile CVE databases and flaky external network dependencies.

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

## 4. Remote Stack Filesystem & Runtime Assets

Active deployments offer a comprehensive remote filesystem manager for `~/.docker-orkes/stacks/<stack-name>/`:
- **Directory Tree & Breadcrumbs**: Navigate subfolders with clickable breadcrumb paths.
- **File & Folder Management**: Create files and subdirectories directly on the target host over SFTP.
- **Drag-and-Drop & Folder Uploads**: Upload single files, multiple files, or entire nested folder hierarchies preserving relative paths (`webkitdirectory`).
- **Remote Archive Extraction**: Extract `.tar.gz` and `.zip` archives directly on the target host via native remote tar/unzip utilities.
- **Live Code Editor with Secret Masking**: View and edit files inline, toggle **Mask Secrets** for `.env` credentials, and trigger one-click **Save & Redeploy** (`docker compose up -d`).
- **Drift Isolation**: Files other than `docker-compose.yml` and `.env` are classified as *Runtime Assets* (e.g., config snippets, certificates, static data) and are excluded from configuration drift detection ([ADR 0008](adr/0008-remote-stack-filesystem-and-runtime-assets.md)).

---

## 5. Host Observability, Telemetry & Logs

When viewing a node's detail page (`/nodes/<id>`), Docker Orkes samples telemetry non-intrusively over SSH:
- **Disk Utilization**: Queries `df -h /` to parse total capacity, consumed gigabytes, free space, and percentage utilization.
- **Docker Engine Vitals**: Inspects `docker info` and queries `docker ps -a --format json` to report container status, health, image tags, and exposed port mappings.
- **Live Container Logs**: View stdout/stderr logs directly from any container running on the target node via `docker logs --tail 200`.

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

---

## 7. Deployment Revisions & Forward-Only Rollback

To guard against misconfigurations or bad updates:
- **Immutable Snapshots**: Every deployment action automatically snapshots the `docker-compose.yml` content, `.env` key-values, and timestamp into a versioned revision entry ([ADR 0005](adr/0005-forward-only-deployment-revisions-with-bounded-retention.md)).
- **Forward-Only Rollback**: Rolling back to a previous revision does not delete history; instead, it generates a new forward-moving revision with the restored configuration and deploys it immediately to the target node.
- **Bounded Retention**: Retention is capped (default: 20 revisions per deployment) to keep SQLite storage lean and deterministic.

---

## 8. Proactive Liveness Poller & Multi-Channel Alerting

Rather than relying purely on manual operator observation:
- **Background Poller**: A lightweight background poller audits node reachability, container states, and configuration drift at configurable intervals (e.g. every 2 minutes) without daemon overhead ([ADR 0004](adr/0004-lightweight-poller-and-edge-triggered-alerting.md)).
- **Edge-Triggered Alerting**: Alerts fire only on state transitions (e.g., `online` → `offline` or `healthy` → `unhealthy`), preventing alert fatigue.
- **Dispatch Channels**: Supports Telegram Bots, Discord Webhooks, and generic JSON HTTP webhooks configured directly from the Control Plane Settings.

---

## 9. 100% Offline Interactive Web Terminal (PTY)

Operators can initiate low-latency terminal sessions directly inside the web browser without any third-party SaaS or internet dependencies ([ADR 0009](adr/0009-offline-web-terminal-pty.md)):
- **Dual Target Modes**:
  1. **Host Terminal**: Opens an interactive SSH PTY session into the managed node's host shell.
  2. **Container Terminal**: Executes an interactive PTY session directly inside any active container (`docker exec -it <container> /bin/sh`) with automatic shell detection (`/bin/bash` with fallback to `/bin/sh`).
- **Fully Bundled Client Assets**: Terminal emulation is powered by bundled `xterm.js` and `addon-fit` served locally from Go's embedded filesystem (`embed.FS`).
- **Safety & Lifecycle**: Terminal sessions enforce a 30-minute idle inactivity timeout, sanitize container identifiers against shell injection, and dynamically synchronize window resize dimensions (`SIGWINCH`) over WebSocket.

---

## 10. Disaster Recovery & Standby Replication

Docker Orkes supports an **Active-Passive (Warm Standby)** replication topology ([ADR 0006](adr/0006-active-passive-control-plane-replication.md)):
- **Autonomous Nodes**: Target hosts and running containers do not depend on continuous Control Plane connectivity. If the primary Control Plane goes down, workloads continue running uninterrupted.
- **Unified Identity**: The SSH private key is stored inside the SQLite database, meaning database replication replicates SSH access automatically without altering `~/.ssh/authorized_keys` on nodes.
- **Split-Brain Immunity**: The standby instance remains stopped, ensuring only one poller runs and dispatches alerts. See [Disaster Recovery Guide](disaster-recovery.md) for step-by-step failover instructions.

---

## 11. Internal Hybrid Image Registry Model

For air-gapped or bandwidth-constrained environments, Docker Orkes incorporates an architectural pattern for an internal OCI registry ([ADR 0007](adr/0007-internal-hybrid-image-registry.md)):
- Deployed as a standard Docker Compose stack on an designated node.
- Provides private image hosting alongside pull-through caching for upstream registries (Docker Hub, GitHub Container Registry) to speed up deployments across node clusters.
- For complete step-by-step deployment instructions, compose configuration, and daemon setup, see the [Internal Hybrid Image Registry Setup Guide](internal-registry.md).
