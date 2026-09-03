# 04 — Deployment Lifecycle (Deploy, Stop, Restart via SFTP & SSH)

**What to build:** An operator can bind a Stack template to a target Node (creating a Deployment), configure node-specific environment variables, deploy the stack with real-time terminal output streaming in the browser, and manage lifecycle states (`restart`, `down`).

**Blocked by:** 
- 02 — Node Inventory & SSH Connectivity Test
- 03 — Stack Templates & Compose Editor

**Status:** ready-for-agent

- [ ] Operator can create a Deployment by choosing a Stack and selecting a target Node.
- [ ] Operator can override or populate environment variables specific to this target Node before deploying.
- [ ] Deploy action establishes an SFTP session to ensure the remote Stack Directory exists at `~/.docker-orkes/stacks/<stack-name>/` and writes the `docker-compose.yml` and `.env` files.
- [ ] Control Plane executes `docker compose -f ~/.docker-orkes/stacks/<stack-name>/docker-compose.yml up -d` over SSH and streams standard output and standard error in real time to the browser via SSE or WebSocket.
- [ ] Deployment status transitions appropriately (`deploying`, `running`, `failed`, `stopped`) and is reflected in the dashboard.
- [ ] Operator can trigger `restart` and `down` actions directly from the Deployment detail view, with streaming confirmation output.
- [ ] Automated tests verify Deployment creation, file transfer simulation, command argument composition, and status updates via the HTTP boundary.
