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

**Pre-flight Check**:
An interactive evaluation comparing Desired State against Actual State, detailing configuration diffs and container impacts presented to an operator immediately prior to executing a Deployment mutation.
_Avoid_: Dry run scan, pre-deploy verification, diff preview


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

**Node Image**:
An OCI container image stored locally within the Docker daemon storage of a specific Node.
_Avoid_: Remote image, cached image, registry artifact

**On-Demand Image**:
A Node Image explicitly pinned by an operator to indicate intentional retention for scheduled or ad-hoc tasks (such as database backups or migration runners) despite having no continuously running container.
_Avoid_: Standby image, orphan image, idle image

**Managed Container**:
A container running on a Node that is instantiated, tracked, and managed by the Control Plane as part of a Deployment.
_Avoid_: Internal container, orkes container, owned container

**Unmanaged Container**:
A container running on a Node that was created outside the Control Plane (such as via manual `docker run`, host system services, or external Compose files) and is not linked to any active Deployment.
_Avoid_: Rogue container, alien container, external container, foreign container

**Image Update**:
The availability of a newer OCI image manifest or digest in an upstream registry for an existing Node Image repository tag.
_Avoid_: Image patch, container upgrade, hotfix

**Deployment Rollout**:
The recreation and startup of a Deployment's containers using newly pulled image layers while maintaining configuration and environment variables.
_Avoid_: Hard restart, wipe deploy, reinstall

**Topology Map**:
A visual, hierarchical graph rendered in the Control Plane dashboard showing the relationships connecting the Control Plane, managed Nodes, active Deployments, and individual runtime containers.
_Avoid_: Cluster diagram, dependency graph, infrastructure chart

**Node Volume**:
A named Docker storage volume managed by the Docker daemon on a specific Node.
_Avoid_: Disk mount, host folder, raw volume

**Node Network**:
A Docker network bridge or overlay managed by the Docker daemon on a specific Node.
_Avoid_: Subnet, host interface, IP bridge

**Managed Volume / Managed Network**:
A volume or network created and labeled by Docker Compose as part of a Deployment Stack.
_Avoid_: Stack storage, internal network

**Unmanaged Volume / Unmanaged Network**:
A volume or network created outside the Control Plane (via manual `docker volume create` / `docker network create` or external tools).
_Avoid_: External volume, orphan network, foreign storage

**Deployment Deletion**:
The removal of a Deployment record from the Control Plane, with operator-selected lifecycle actions on the target Node (teardown or detach).
_Avoid_: Stack uninstall, purge instance, cluster removal

**Detached Deployment**:
A Deployment removed from the Control Plane without tearing down Node resources, leaving existing containers running as Unmanaged Containers and volumes intact.
_Avoid_: Orphaned deployment, unlinked stack

**Teardown**:
The execution of `docker compose down` on a Node via the SSH Executor to stop and remove containers and networks associated with a Deployment.
_Avoid_: Destroy, wipe, uninstall

**Data Purge**:
The explicit, destructive removal of Managed Volumes and Stack Directory contents on a Node during Deployment Deletion, requiring typed safety confirmation.
_Avoid_: Hard delete, format, scrub

**Force Deletion**:
The removal of a Deployment metadata record from the Control Plane when the target Node is unreachable, bypassing remote SSH cleanup.
_Avoid_: Hard kill, bypass delete, ignore offline

**Deployment Adoption**:
The process of attaching the Control Plane to existing Unmanaged Containers and volumes on a Node, reconciling them with a Desired State without unnecessary recreation.
_Avoid_: Container import, hijack, take over

**In-Place Convergence**:
The alignment of a Node's runtime state to a Desired State using `docker compose up -d --no-recreate`, preserving running containers that already match configuration and protecting existing Node Volumes.
_Avoid_: Soft sync, non-destructive deploy, graceful reload






