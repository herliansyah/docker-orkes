# Internal Hybrid Image Registry Setup Guide

This guide provides step-by-step instructions for deploying and configuring an internal, lightweight OCI container image registry using **Zot** on Docker Orkes, according to [ADR 0007](adr/0007-internal-hybrid-image-registry.md).

---

## 1. Overview & Architecture

In a multi-node environment, nodes repeatedly download container images (e.g. `nginx`, `postgres`, `redis`, base OS images). Pulling directly from Docker Hub repeatedly leads to:
- Docker Hub anonymous rate limits (100 pulls per 6 hours).
- Increased outbound internet bandwidth costs.
- High download latency across remote nodes.

Docker Orkes uses **Zot** (`ghcr.io/project-zot/zot-linux-amd64`) deployed as a standard Stack on a designated infrastructure Node.

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
                    │  - Local Private Repos │
                    │  - Pull-Through Cache  │
                    └───────┬────────┬───────┘
            Local Pulls     │        │    Local Pulls
        ┌───────────────────┘        └───────────────────┐
        ▼                                                ▼
┌─────────────────┐                            ┌─────────────────┐
│     Node 1      │                            │     Node 2      │
│  (Docker Host)  │                            │  (Docker Host)  │
│  daemon.json -> │                            │  daemon.json -> │
│  registry-mirror│                            │  registry-mirror│
└─────────────────┘                            └─────────────────┘
```

### Key Advantages
1. **Single-Container Simplicity**: Zot runs as a standalone single-binary container consuming only ~30MB RAM. No separate database, Redis, or auxiliary workers needed.
2. **Dual-Functionality**:
   - **Pull-Through Cache**: Automatically caches upstream Docker Hub images on first pull. Subsequent pulls by any node hit local disk at LAN speed (1–10 Gbps).
   - **Private Image Hosting**: Serves as the primary push/pull target for custom, proprietary images built in your CI/CD pipelines.
3. **100% Agentless & Declarative**: Managed entirely as a Docker Orkes Stack with files organized under `~/.docker-orkes/stacks/internal-registry/`.

---

## 2. Step 1: Create the Stack Template

1. Open the Docker Orkes dashboard (`http://localhost:8080`).
2. Navigate to the **Stack Templates** tab.
3. Click **+ Add Stack** and fill in:
   - **Slug Name**: `internal-registry`
   - **Notes**: `Lightweight Zot OCI Registry with on-demand Docker Hub pull-through cache (ADR 0007)`.
4. In the **Compose Configuration (`docker-compose.yml`)** box, enter:

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

5. In the **Default Environment Variables (`.env`)** box, enter:
```env
REGISTRY_PORT=5000
```
6. Click **Save Stack Template**.

---

## 3. Step 2: Deploy to the Infrastructure Node

1. Navigate to the **Active Deployments** tab.
2. Click **+ New Deployment**.
3. Select:
   - **Target Node**: Choose your designated infrastructure or storage node (e.g. `node-infra` or `node1`).
   - **Stack Template**: `internal-registry`.
4. Click **Deploy to Node**.

---

## 4. Step 3: Configure Zot via Runtime Assets

Zot requires a configuration file (`config.json`) defining the pull-through cache settings. You can create this file directly inside the Docker Orkes web UI:

1. On the **Active Deployments** tab, locate `internal-registry` and click the **📁 Files** button.
2. Click **+📄 File** in the toolbar.
3. Enter the filename: `config.json`.
4. Paste the following configuration into the live editor:

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
> If you have a paid Docker Hub account or want higher pull limits when caching from Docker Hub, you can optionally include your Docker Hub credentials in the sync registry definition:
> ```json
> "credentials": {
>   "username": "your-dockerhub-user",
>   "password": "your-dockerhub-pat"
> }
> ```

5. Click **🚀 Save & Redeploy**.
   Docker Orkes will save `config.json` over SFTP and trigger `docker compose up -d` to restart Zot with the new configuration.

---

## 5. Step 4: Configure Remote Worker Nodes

To enable transparent image caching without modifying application compose files, configure the Docker daemon on all managed nodes to use Zot as a registry mirror.

You can perform this directly using Docker Orkes' **Host Terminal** on each node:

1. In the **Managed Nodes** tab, click **Terminal** on the worker node.
2. Edit `/etc/docker/daemon.json` (replace `192.168.1.50` with your Registry Node's IP):

```bash
sudo mkdir -p /etc/docker
cat << 'EOF' | sudo tee /etc/docker/daemon.json
{
  "registry-mirrors": ["http://192.168.1.50:5000"],
  "insecure-registries": ["192.168.1.50:5000"]
}
EOF
```

3. Reload and restart the Docker engine:
```bash
sudo systemctl daemon-reload
sudo systemctl restart docker
```

4. Verify that Docker has recognized the mirror:
```bash
docker info | grep -A 3 "Registry Mirrors"
```
Output should show:
```
 Registry Mirrors:
  http://192.168.1.50:5000/
```

---

## 6. Verification & Usage Scenarios

### Scenario A: Automatic Pull-Through Caching
From any configured worker node, pull an image:
```bash
docker pull alpine:latest
```
The Docker daemon requests the image from `http://192.168.1.50:5000`. Zot fetches it from Docker Hub, saves it to its local volume (`/var/lib/registry`), and streams it to the node.

Pulling it on a second node will now load directly from the local registry at maximum LAN throughput, with zero traffic to Docker Hub!

### Scenario B: Pushing Custom Private Application Images (CI/CD)
To push proprietary microservices to the internal registry:

1. **Tag and Push to Zot**:
   ```bash
   docker build -t 192.168.1.50:5000/myteam/web-app:v1.0.0 .
   docker push 192.168.1.50:5000/myteam/web-app:v1.0.0
   ```

2. **Dual-Push to Backup Registry (ADR 0007)**:
   In accordance with the dual-push operational pattern, push the same image to an offsite registry (e.g. Docker Hub or GitHub Container Registry) as an offsite DR backup:
   ```bash
   docker tag 192.168.1.50:5000/myteam/web-app:v1.0.0 myorg/web-app:v1.0.0
   docker push myorg/web-app:v1.0.0
   ```

3. **Reference in Docker Orkes Stacks**:
   In your `docker-compose.yml` templates:
   ```yaml
   services:
     web:
       image: 192.168.1.50:5000/myteam/web-app:v1.0.0
       ports:
         - "80:80"
   ```

---

## 7. Disaster Recovery & Fallback Behavior

- **If the Registry Node goes offline**:
  - Worker nodes will attempt to contact `192.168.1.50:5000`. When unreachable, Docker's native daemon automatically falls back to pulling public images directly from upstream (`docker.io`).
  - For proprietary private images, update the deployment's stack template to use the backup repository (e.g. `myorg/web-app:v1.0.0`) and trigger **Save & Redeploy**.
- **Data Persistence**:
  - Registry images are stored under `~/.docker-orkes/stacks/internal-registry/data/` on the registry node. Periodic backups can be taken using standard rsync or volume snapshots.
