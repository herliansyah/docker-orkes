# Disaster Recovery & Standby Control Plane

This document describes the disaster recovery architecture and operational runbook for Docker Orkes using an **Active-Passive (Warm Standby)** setup.

---

## 1. Architectural Overview

```
+------------------------------------+             +------------------------------------+
|       PRIMARY CONTROL PLANE        |             |       STANDBY CONTROL PLANE        |
|  - Host IP: 192.168.1.10           |             |  - Host IP: 192.168.1.20           |
|  - docker-orkes: RUNNING           |             |  - docker-orkes: STOPPED           |
|  - Poller: ACTIVE (every 2m)       |             |  - Poller: INACTIVE                |
|  - Database: docker-orkes.db       |             |  - Database: docker-orkes.db (sync)|
+-----------------+------------------+             +------------------^-----------------+
                  |                                                   |
                  | (Every 15 min via cron)                           |
                  | 1. sqlite3 .backup -> temp.db                     |
                  +----------------------- rsync -az -----------------+
                                          |
                                          v
                              +-----------------------+
                              |      REMOTE NODE      |
                              |  - Containers running |
                              |  - authorized_keys    |
                              +-----------------------+
```

### Key Principles

1. **Autonomous Workload Execution**:
   Because Docker Orkes uses agentless SSH ([ADR 0001](adr/0001-agentless-ssh-architecture.md)), Docker containers running on remote Nodes are completely decoupled from Control Plane availability. If the Primary server fails, all containers continue operating without interruption.

2. **Unified Cryptographic Identity**:
   The Control Plane SSH keypair is stored directly within the SQLite database (`settings` table via `EnsureSSHKeyPair`). Replicating the database automatically replicates the exact same private key. **You do not need to update `~/.ssh/authorized_keys` on any remote Node.**

3. **Split-Brain Prevention**:
   The `docker-orkes` service on the Standby server remains **strictly stopped** until manual promotion. This ensures that only one Poller inspects remote nodes and dispatches alert notifications at any given time.

4. **Firewall / Security Group Consideration**:
   If remote Nodes enforce firewall rules (e.g. UFW or cloud Security Groups) restricting SSH access (port 22) to specific IPs, ensure the **Standby server's IP address** is also whitelisted on all remote Nodes.

---

## 2. Setup Replication (Primary -> Standby)

### Step 1: Configure SSH Access from Primary to Standby
On the **Primary** server, ensure a dedicated system user can SSH into the **Standby** server without interactive passwords:

```bash
# Generate backup deploy key on Primary (if not already present)
ssh-keygen -t ed25519 -f ~/.ssh/id_dr_backup -N ""

# Copy the public key to Standby server
ssh-copy-id -i ~/.ssh/id_dr_backup.pub orkes-backup@standby-host
```

### Step 2: Create Backup & Sync Script on Primary
Create `/usr/local/bin/orkes-sync-standby.sh` on the **Primary** server:

```bash
#!/usr/bin/env bash
set -euo pipefail

# Configuration
DATA_DIR="/opt/docker-orkes"
DB_FILE="${DATA_DIR}/docker-orkes.db"
STANDBY_USER="orkes-backup"
STANDBY_HOST="192.168.1.20"
STANDBY_DIR="/opt/docker-orkes"
SSH_KEY="/home/orkes/.ssh/id_dr_backup"
TMP_BACKUP="/tmp/docker-orkes-backup.db"

# 1. Non-blocking SQLite online backup (guarantees safe snapshot even in WAL mode)
sqlite3 "${DB_FILE}" ".backup '${TMP_BACKUP}'"

# 2. Transfer the snapshot to Standby server
rsync -az -e "ssh -i ${SSH_KEY} -o StrictHostKeyChecking=accept-new" \
  "${TMP_BACKUP}" \
  "${STANDBY_USER}@${STANDBY_HOST}:${STANDBY_DIR}/docker-orkes.db"

# 3. Clean up temporary snapshot
rm -f "${TMP_BACKUP}"
```

Make the script executable:
```bash
chmod +x /usr/local/bin/orkes-sync-standby.sh
```

### Step 3: Enable Cron Job on Primary
Add the cron job to run every 15 minutes:

```bash
crontab -e
```

Add the following line:
```cron
*/15 * * * * /usr/local/bin/orkes-sync-standby.sh > /var/log/orkes-sync.log 2>&1
```

---

## 3. Emergency Failover Runbook (Promote Standby)

When the Primary server experiences hardware failure, network disconnection, or host corruption:

### Step 1: Confirm Primary is Down
Ensure that the Primary server is genuinely unreachable to avoid confusion:
```bash
ping -c 3 192.168.1.10 || echo "Primary is offline"
```

### Step 2: Start Control Plane on Standby Server
SSH into the **Standby** server (`192.168.1.20`) and start the service:

**If running via systemd:**
```bash
sudo systemctl start docker-orkes
sudo systemctl status docker-orkes
```

**If running via Docker Compose:**
```bash
cd /opt/docker-orkes
docker compose up -d
docker compose logs -f
```

### Step 3: Switch Network / DNS Access
- **If using DNS** (e.g. `orkes.internal.corp`): Update the A record pointing from `192.168.1.10` to `192.168.1.20`.
- **If using Direct IP**: Open `http://192.168.1.20:8080` in your browser.

### Step 4: Verify Node Connectivity
1. Log in to the dashboard on the Standby server.
2. Check the **Nodes** list. All registered nodes will be present.
3. Click **Sync All** or wait for the background Poller (within 2 minutes). Nodes should transition to `online`.

---

## 4. Post-Incident Failback (Restoring Primary)

Once the original Primary server is repaired or replaced:

1. **Stop** `docker-orkes` on the Standby server:
   ```bash
   sudo systemctl stop docker-orkes
   ```
2. Sync the latest database back to the Primary server:
   ```bash
   sqlite3 /opt/docker-orkes/docker-orkes.db ".backup /tmp/failback.db"
   rsync -az /tmp/failback.db orkes@192.168.1.10:/opt/docker-orkes/docker-orkes.db
   rm -f /tmp/failback.db
   ```
3. Start `docker-orkes` on the Primary server:
   ```bash
   sudo systemctl start docker-orkes
   ```
4. Re-enable the 15-minute cron job on the Primary server.
5. Revert DNS back to the Primary server IP.
