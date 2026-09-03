Status: ready-for-agent

# Specification: Docker Orkes Control Plane

## Problem Statement

Managing Docker Compose applications across multiple remote servers is fragmented, error-prone, and lacks central visibility. Users currently have to manually SSH into individual servers, copy or edit `docker-compose.yml` and `.env` files, run `docker compose up -d`, and remember which server is running which applications. 

When a compose file is modified directly on a server (or updated centrally), there is no automated way to detect configuration drift or compare changes before deployment. Furthermore, checking basic server vitals (such as disk storage capacity and running containers) requires manual terminal sessions across multiple hosts, and there is no single place that tracks or backs up stack definitions and deployment states into Git.

## Solution

Docker Orkes provides a lightweight, agentless multi-node deployment and observability control plane. Built as a single binary with an embedded web dashboard and SQLite database, it connects to remote Docker nodes via SSH without requiring any custom background daemon installed on target servers.

Users can define reusable Stack templates (`docker-compose.yml` and default environment variables), deploy them to one or more Nodes with node-specific environment configurations, inspect disk usage and container statuses on demand, detect configuration drift between the control plane and remote servers via visual side-by-side diffs, and automatically maintain a versioned Git mirror of all configurations.

## User Stories

1. As a system administrator, I want to add a remote Node by specifying its hostname/IP, SSH port, and SSH user, so that the Control Plane can communicate with it.
2. As a system administrator, I want the Control Plane to generate an Ed25519 SSH keypair and display its public key in the UI, so that I can easily authorize it in `~/.ssh/authorized_keys` on my remote servers without managing custom keys.
3. As a system administrator, I want to test the SSH connection to a Node from the UI, so that I know immediately whether authentication and Docker socket permissions succeed.
4. As a developer, I want to create a Stack template by uploading or pasting a `docker-compose.yml` file, so that I have a reusable application definition.
5. As a developer, I want to define default environment variables for a Stack, so that common configuration values are documented and pre-filled.
6. As a developer, I want to assign a Stack to a specific Node (creating a Deployment) and provide node-specific environment variables, so that I can customize configurations per server environment without altering the core compose file.
7. As an operator, I want the Control Plane to automatically copy the compose file and resolved environment variables over SFTP to a standardized Stack Directory (`~/.docker-orkes/stacks/<stack-name>/`) on the target Node, so that stack files remain clean and organized.
8. As an operator, I want to trigger a deployment action (`up`, `down`, `restart`, `pull`) from the web dashboard, so that I do not need to open a manual SSH terminal session.
9. As an operator, I want to see streaming command execution output when deploying a Stack, so that I can monitor build/pull progress and debug failures in real time.
10. As an operator, I want to view the list of all active Deployments on a selected Node, so that I know exactly which Stacks are deployed to which servers.
11. As a system administrator, I want to view the total and available disk storage capacity (HDD/SSD) of each Node in the dashboard, so that I can prevent servers from running out of disk space.
12. As a system administrator, I want to view all running and stopped containers on a Node along with their resource usage (CPU and memory), so that I can assess server health at a glance.
13. As an operator, I want to run a Drift Check on a Deployment, so that the Control Plane computes SHA-256 checksums of the remote files against the Desired State and flags whether the remote files were modified externally.
14. As an operator, I want to view a side-by-side visual line diff when a Deployment has drifted, so that I can inspect exactly what changed on the remote server before taking action.
15. As an operator, I want the option to reconcile a drifted Deployment by overwriting the remote files with the Desired State and redeploying, so that I can restore intended configuration.
16. As an operator, I want the option to adopt remote changes into the Desired State, so that intentional hotfixes made directly on the remote server can be preserved in the Control Plane.
17. As an operator, I want to view container logs for any running container on a remote Node through the dashboard, so that I can troubleshoot application errors quickly.
18. As a backup administrator, I want the Control Plane to push updated Stack definitions and Deployment metadata to a designated Git Mirror repository, so that all infrastructure definitions are version-controlled and backed up off-site.
19. As a system administrator, I want access to the Web Dashboard to be protected by a master password or session token, so that unauthorized users on the network cannot control remote servers.
20. As a user, I want the dashboard to fetch host metrics and container statuses reactively on-demand when I open the page, so that the Control Plane does not create excessive SSH connection storms when idle.

## Implementation Decisions

- **Single Static Go Binary**: The entire Control Plane runs as a single Go binary compiling the backend server, embedded database engine, and frontend assets.
- **Embedded SQLite Storage**: Persistence is managed by an embedded, CGO-free SQLite database storing Nodes, Stacks, Deployments, and system configuration.
- **Agentless SSH Transport (SSH Executor)**: All interactions with remote Nodes (file transfers, lifecycle command invocations, host metrics queries) execute over standard SSH and SFTP connections using native cryptography libraries. Target Nodes require only standard OpenSSH and Docker Engine with user access to the Docker socket.
- **Standard Remote Filesystem Structure**: Remote compose files and `.env` files are exclusively stored in isolated directories under the user's home path (`~/.docker-orkes/stacks/<stack-name>/`), avoiding root permission requirements.
- **Decoupled Stack and Deployment Model**: Stacks are modeled as reusable templates. A Deployment represents the binding of a Stack to a Node with resolved node-specific environment variables.
- **Checksum and Diff-Based Drift Detection**: Drift detection reads remote file contents, compares SHA-256 digests against the Desired State stored in SQLite, and generates standard unified line diffs for visual review.
- **Git Mirror as Asynchronous Backup**: The Control Plane acts as the primary editing interface, while maintaining an external Git repository as an audit log and versioned backup structured into `/stacks/` and `/deployments/` directories.
- **On-Demand Observability**: Real-time metrics (disk space, container lists, container resource usage) are streamed to the browser using Server-Sent Events (SSE) upon active user dashboard sessions, with relaxed periodic heartbeat checks when inactive.
- **Single Master Token Authentication**: The Web Dashboard enforces a session cookie or bearer token based on a configurable master password.

## Testing Decisions

- **Boundary / Highest Seam Testing**: Testing focuses strictly at the HTTP/Web API boundary. HTTP handlers receive requests and their responses and database side-effects are asserted against expected state.
- **Loopback / In-Memory SSH Transport**: An abstracted SSH execution interface allows tests to inject an in-memory loopback SSH runner without requiring physical remote servers or real network connections.
- **External Behavior Assertions**: Tests assert HTTP status codes, correct persistence in SQLite, correct generation of compose and environment payloads sent to the transport layer, and accurate unified diffs when simulated remote content deviates from desired state.

## Out of Scope

- Multi-tenant Role-Based Access Control (RBAC) with granular user permissions.
- Docker Swarm or Kubernetes orchestration (scheduling containers across nodes, distributed overlay ingress routing).
- Persistent volume synchronization across nodes (distributed network storage like Ceph or NFS).
- Automatic aggressive reconciliation loops that overwrite remote files without user confirmation.

## Further Notes

- Target nodes must have Docker Compose plugin (`docker compose`) installed and the SSH user must belong to the `docker` group or have permissions to access `/var/run/docker.sock`.
