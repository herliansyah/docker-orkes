# Docker Orkes

A multi-node Docker deployment and host observability control plane based on declarative Docker Compose stacks.

## Language

**Control Plane**:
The central service that stores stack configurations, tracks node states, initiates deployments, and monitors remote host metrics.
_Avoid_: Master, orchestrator, manager

**Node**:
A remote server running a Docker daemon managed by the Control Plane.
_Avoid_: Server, target, agent machine, host

**Stack**:
A reusable application template defined by a `docker-compose.yml` file and default environment variable definitions.
_Avoid_: Compose project, service, bundle

**Deployment**:
An active instantiation of a Stack assigned to a specific Node, containing the resolved environment variables and runtime state.
_Avoid_: Instance, release, assignment

**Desired State**:
The intended configuration and compose specification for a Deployment as stored in the Control Plane and synced to the Git Mirror.
_Avoid_: Target config, master compose

**Actual State**:
The compose configuration and container runtime state currently existing and running on a specific Node.
_Avoid_: Live state, remote status

**Drift**:
A discrepancy between the Desired State and the Actual State on a Node.
_Avoid_: Conflict, sync mismatch, diff

**Drift Check**:
The verification operation that compares SHA-256 checksums and generates line diffs between the Desired State and the Actual State on a Node.
_Avoid_: Diff scan, sync check


**Git Mirror**:
An external Git repository maintained by the Control Plane as a versioned backup and audit log of all Stacks and Node assignments.
_Avoid_: GitOps repo, upstream repo

**SSH Executor**:
The agentless transport mechanism within the Control Plane that executes Docker commands, transfers compose files, and samples host metrics over secure shell.
_Avoid_: Agent, runner, worker daemon

**Stack Directory**:
The dedicated filesystem path on a Node (`~/.docker-orkes/stacks/<stack-name>/`) where a Stack's `docker-compose.yml` and `.env` files are stored and executed.
_Avoid_: App folder, project path, workspace

**Revision**:
A versioned, immutable snapshot of a Deployment's Desired State (compose configuration, environment variables, and timestamp) captured at deployment time to enable rollbacks.
_Avoid_: Release, backup, commit, checkpoint

**Poller**:
The background routine inside the Control Plane that periodically audits Node reachability, container health, and Drift without manual operator action.
_Avoid_: Daemon, watcher, heartbeat agent, cron job

**Notification Channel**:
An outbound messaging integration (such as an HTTP webhook, Telegram bot, or Discord channel) configured in the Control Plane to dispatch alerts when a Node goes offline, a container crashes, or Drift is detected.
_Avoid_: Alert manager, notifier service, webhook emitter

**Primary Control Plane**:
The active Control Plane instance that serves the dashboard, runs the background Poller, and executes deployments against remote Nodes.
_Avoid_: Active node, master node, main server

**Standby Control Plane**:
A passive replica instance of the Control Plane that receives periodic database state updates from the Primary Control Plane and remains stopped to prevent split-brain polling until manually promoted during an incident.
_Avoid_: Backup node, slave, secondary worker

**Image Registry**:
An internal OCI-compliant registry running as a Stack on a designated Node that provides both local repository storage for custom application images and pull-through caching for upstream registries.
_Avoid_: Docker cache, registry proxy, mirror server

**Runtime Assets**:
Files and directories located inside a Stack Directory on a Node other than `docker-compose.yml` and `.env` files (such as auxiliary configuration files, static web assets, and SSL certificates) that are managed via remote file operations and not tracked for Drift.
_Avoid_: Extra files, static data, auxiliary blobs

**Web Terminal**:
An interactive, browser-based pseudo-terminal (PTY) session bridged over secure WebSocket to a Node host shell or a running container for emergency inspection and debugging, operating entirely offline without external CDN dependencies.
_Avoid_: Web shell, remote console, back door



