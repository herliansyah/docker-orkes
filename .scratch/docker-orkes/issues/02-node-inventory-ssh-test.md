# 02 — Node Inventory & SSH Connectivity Test

**What to build:** An operator can register, list, and delete remote Nodes (specifying name, IP/hostname, SSH port, and SSH user) through the dashboard, and click an interactive "Test Connection" button to verify SSH connectivity and Docker daemon responsiveness with real-time status indicators.

**Blocked by:** 01 — Server Foundation, Auth, & Keypair Initialization

**Status:** ready-for-agent

- [ ] Operator can add a new Node by providing a friendly name, IP/hostname, SSH port (default 22), and SSH username.
- [ ] Nodes are listed on the main inventory screen showing their connection status and basic connection details.
- [ ] Clicking "Test Connection" on a Node triggers an asynchronous SSH handshake using the Control Plane's private key, tests running `docker version`, and updates the Node's status badge (Online, Unreachable, or Permission Denied).
- [ ] Meaningful error messages are displayed if SSH authentication fails or if the remote user lacks Docker socket permissions.
- [ ] Operator can remove a Node from the inventory, confirming before deletion.
- [ ] Automated tests verify node persistence and simulated SSH connection success/failure through the HTTP boundary.
