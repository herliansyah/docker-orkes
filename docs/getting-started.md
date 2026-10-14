# Getting Started with Docker Orkes

This guide walks you through setting up and running **Docker Orkes** to manage your multi-node Docker deployments.

---

## 1. Prerequisites

Before starting, ensure you have:
1. **Control Plane Host**: A machine (local workstation or VPS) with Linux or macOS, running Go 1.22+ if compiling from source.
2. **Target Nodes**: One or more remote Linux servers with:
   - Docker Engine installed (`docker compose version` available).
   - SSH server running and reachable.
   - User account with permission to run `docker` commands (either `root` or a user in the `docker` group).

---

## 2. Installation and Running

### Building from Source
```bash
git clone https://github.com/herliansyah/docker-orkes.git
cd docker-orkes
go build -o bin/docker-orkes ./cmd/docker-orkes
```

### Starting the Control Plane
```bash
./bin/docker-orkes -addr :8080 -pass your-secure-password
```

Or via environment variables:
```bash
export ADDR=":8080"
export MASTER_PASSWORD="your-secure-password"
export DB_PATH="docker-orkes.db"
export LOG_FILE="docker-orkes.log"
export LOG_LEVEL="info"
./bin/docker-orkes
```

Open `http://localhost:8080` in your web browser and sign in with the password you specified.

---

## 3. Authorizing Remote Nodes

When Docker Orkes starts for the first time, it automatically generates a persistent 4096-bit RSA key pair in the database (`docker-orkes.db`).

You can authorize target nodes in one of two ways:

### Option A: Automatic Key Injection (Recommended)
1. On the dashboard, go to the **Managed Nodes** tab.
2. Click **+ Add Node**.
3. Fill in the server name, hostname or IP, SSH port, and user (e.g. `root`).
4. In the **⚡ Auto-Inject SSH Key via Password** field, enter the remote server's SSH password.
5. Click **Save & Setup Node**. Docker Orkes will connect once, append its public key to `~/.ssh/authorized_keys`, and verify the connection. The password is never saved to the database.

### Option B: Manual Public Key Authorization
1. Click **⚙️ Settings** in the top navigation bar.
2. Click **Copy Key** on the **Control Plane SSH Public Key** card.
3. On your target server, append the copied key to `~/.ssh/authorized_keys`:
   ```bash
   echo "ssh-rsa AAAA..." >> ~/.ssh/authorized_keys
   chmod 600 ~/.ssh/authorized_keys
   ```
4. On the dashboard, click **+ Add Node**, enter your node details without a password, and click **Save & Setup Node**.
5. In the Actions menu for the node, click **Test Connection** to confirm connectivity.

---

## 4. Creating a Stack Template

1. Navigate to the **Stack Templates** tab.
2. Click **+ Add Stack**.
3. Enter a slug name (e.g. `nginx-proxy` or `redis-cache`).
4. Paste your `docker-compose.yml` configuration:
   ```yaml
   services:
     web:
       image: nginx:alpine
       ports:
         - "${PORT:-80}:80"
       restart: always
   ```
5. Specify default environment variables (e.g. `PORT=8080`) and optional notes.
6. Click **Save Stack Template**.

---

## 5. Deploying Stacks to Nodes

1. Navigate to the **Active Deployments** tab.
2. Click **+ New Deployment**.
3. Select the stack template and the target node.
4. Supply any node-specific environment variables in the `.env` box.
5. Click **Deploy to Node**.

Docker Orkes will:
- Connect to the remote node via SFTP.
- Create the dedicated stack directory `~/.docker-orkes/stacks/<stack-name>/`.
- Upload `docker-compose.yml` and `.env`.
- Execute `docker compose up -d`.
- Store the deployment status, desired state hash, and initial revision snapshot in the database.

> [!WARNING]
> **Data Persistence Rule**: The stack directory `~/.docker-orkes/stacks/<stack-name>/` is strictly for compose templates, environment files, and lightweight auxiliary configs. **Never store database directories or heavy persistent data (e.g. `./data:/var/lib/mysql`) inside this directory**, as it is completely removed (`rm -rf`) if an operator chooses "Purge Data" during deployment deletion. Always use Docker Named Volumes or absolute host paths outside `~/.docker-orkes/` (e.g. `/data/...` or `/mydocker/...`) for persistent data.

---

## 6. Configuring Git Mirror Backup (Optional)

1. Click **⚙️ Settings** in the top navigation bar.
2. In the **Git Mirror Backup Sync** card, specify your remote Git repository URL:
   - Example SSH: `git@github.com:myorg/docker-infra.git`
   - Example HTTPS: `https://github.com/myorg/docker-infra.git`
3. Set the target branch (e.g. `main`).
4. If using SSH, copy the Control Plane SSH Public Key and add it as a **Deploy Key with Write Access** in your GitHub/GitLab repository settings.
5. Click **Save & Sync Mirror**. Docker Orkes will commit all stacks and deployment manifests and push them to your repository.

---

## 7. Managing Remote Files & Runtime Assets

1. On the **Active Deployments** table, click the **📁 Files** button on any deployment.
2. Browse files and folders under `~/.docker-orkes/stacks/<stack-name>/`.
3. Create auxiliary files (e.g. `nginx.conf`) or subdirectories (`+File` / `+Folder`).
4. Upload files or entire nested directories using the **Upload** menu or by dragging and dropping them into the window.
5. Upload `.tar.gz` or `.zip` archives and click **Extract** to unpack assets on the remote server.
6. Edit configuration files in the integrated editor and click **Save & Redeploy** to reload containers immediately.

---

## 8. Opening Interactive Web Terminal (PTY)

1. **Host Terminal**:
   - On the **Managed Nodes** tab, click **Terminal** on any node card.
   - A low-latency browser terminal connects over WebSocket directly to the remote server shell.
2. **Container Terminal**:
   - On the **Active Deployments** tab or **Node Detail** page, open the **Terminal** dropdown menu and select **Container Terminal**.
   - Pick the target container; Docker Orkes launches an interactive session directly inside the container (`docker exec -it /bin/sh` or `/bin/bash`).

---

## 9. Deployment Revisions & Instant Rollback

1. On the **Active Deployments** tab, click the **📜 Revisions** button on any deployment.
2. Review historical snapshots of compose manifests, environment variables, and timestamps.
3. Click **Rollback** on any previous revision to restore that exact state. A new forward-only revision is created and immediately redeployed to the host node.

---

## 10. Configuring Alerts & Background Poller

1. Click **⚙️ Settings** in the navigation bar.
2. Under **Liveness Poller Settings**, customize the audit polling interval (e.g. `2m`).
3. Under **Notification Channels**, select your alert provider:
   - **Telegram**: Provide Bot Token and Chat ID.
   - **Discord**: Provide Discord Webhook URL.
   - **Custom Webhook**: Provide generic JSON HTTP URL.
4. Click **Send Test Alert** to verify connectivity, then save settings.

---

## 11. Monitoring In-App Activity Console & Live Telemetry Stream

To inspect real-time daemon operations, SSH executions, and system health directly from the web interface without accessing terminal logs:
1. Click the **⚡ Activity** button in the bottom status bar footer (or press `Ctrl+``` anywhere in the interface).
2. The collapsible 6-column Activity Console drawer opens:
   - **Real-Time Stream**: Observe live events across 7 modules (`poller`, `ssh`, `deploy`, `gitsync`, `notifier`, `auth`, `server`).
   - **Pause & Resume**: Click **⏸ Pause** to freeze the incoming feed during active investigations without severing the server stream.
   - **Smart Auto-Scroll**: The viewport automatically sticks to the bottom on new events, and automatically freezes when you scroll up to inspect history.
   - **Filters & Search**: Filter by severity pills (`ALL`, `ERROR`, `WARN`, `INFO`, `DEBUG`) or type in the instant search box to filter by node, module, or message.
   - **Inspect Metadata**: Click on any row with details to expand structured JSON attributes.
   - **Export**: Click **💾 Export** to download client logs instantly as plain text `.log` or structured `.json`.

