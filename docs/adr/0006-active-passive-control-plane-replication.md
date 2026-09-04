# Active-Passive Control Plane Replication for Disaster Recovery

We decided to implement Control Plane disaster recovery using an Active-Passive (Warm Standby) model with native SQLite online backup (`sqlite3 .backup`) and unidirectional `rsync` over SSH, rather than an Active-Active cluster (e.g. Raft/rqlite) or continuous WAL streaming daemons (e.g. Litestream).

Because Docker Orkes uses agentless SSH where container workloads run autonomously on remote Nodes independently of the Control Plane (ADR 0001), Control Plane downtime does not disrupt running user applications. An Active-Active setup would introduce excessive complexity (distributed consensus, shared locks, and high risk of split-brain Poller executions resulting in duplicate alerts or conflicting deployments).

Under this model:
1. The Primary Control Plane runs a periodic cron job (every 15 minutes) using native SQLite online backup to create a non-blocking point-in-time snapshot, then transfers it via `rsync` to the Standby server.
2. The SQLite database inherently stores the Control Plane's Ed25519 SSH keypair (`EnsureSSHKeyPair`), ensuring the Standby server retains the exact same cryptographic identity and can reach existing Nodes without modifying their `authorized_keys`.
3. The Standby Control Plane service remains strictly stopped until manual failover, guaranteeing zero split-brain polling or duplicate alerts.
