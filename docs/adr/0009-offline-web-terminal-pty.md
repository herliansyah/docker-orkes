# Offline Web Terminal Architecture via WebSocket and PTY

We decided to support interactive browser-based terminal access to Managed Nodes and running Stack containers using bidirectional WebSockets and Pseudo-Terminal (PTY) allocation over agentless SSH sessions, bundled with embedded local assets (`xterm.js`) to guarantee operation in air-gapped and offline environments.

## Context & Problem
Operators frequently need to inspect internal container state (e.g. database connections, local logs, migrations) or investigate host-level OS anomalies (e.g. systemd logs, network routing, firewall rules). Previously, operators had to distribute SSH private keys to individual developer machines or exit the Control Plane web interface. Additionally, external CDN dependencies are unacceptable for privacy, security, and air-gapped intranet deployments.

## Decision
1. **Dual Scope (Host & Container Terminal)**: The Web Terminal supports both host-level SSH shell sessions on Managed Nodes (for system administrators) and container exec sessions via `docker exec -it` on running Stack containers (for application developers).
2. **Offline-First Embedded Assets**: Front-end terminal emulation assets (`xterm.js`, `xterm.css`, and fit addon) are embedded directly into the Go binary (`//go:embed`) and served locally via `/static/`. Zero external CDN or internet network calls are made.
3. **Agentless SSH PTY Allocation**: The Control Plane initiates an SSH session to the remote Node, allocates a PTY (`session.RequestPty`), and bridges standard input/output over an authenticated WebSocket (`/ws/terminal`).
4. **Intelligent Container Shell Fallback**: Container exec commands attempt `/bin/bash` first and automatically fall back to `/bin/sh` to ensure compatibility across diverse container images (e.g. Ubuntu vs. Alpine/Busybox).
5. **Strict Session Lifecycle & Cleanup**: When a WebSocket disconnects, the Control Plane sends a termination signal and immediately tears down the remote SSH session and PTY. An idle timeout of 30 minutes terminates inactive sessions to prevent dangling host resources.
6. **Emergency Debugging Scope**: Terminal sessions are designated strictly for live inspection and emergency incident response; any file edits to compose files or environment files remain monitored and flagged as Drift by the background Poller.
