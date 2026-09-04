# Lightweight Liveness Poller and Edge-Triggered Alerting

We decided to implement background health monitoring in the Control Plane using a two-tier polling model with edge-triggered notifications, rather than running continuous full drift checks or continuous alert polling.

The Poller runs a lightweight audit (checking SSH reachability and querying container statuses via Docker CLI) at short intervals (default 2 minutes), while full file-based Drift Checks remain reserved for scheduled longer intervals or operator triggers. Furthermore, notifications to configured channels (Telegram bot and Discord/generic webhooks) fire exclusively on state transitions (`running` -> `failed`, `online` -> `offline`) with corresponding resolution notices when recovered.

This design eliminates SSH connection storms and excessive remote SFTP read overhead across dozens of deployments, while strictly preventing alert fatigue for operators.
