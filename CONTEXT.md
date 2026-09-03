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


