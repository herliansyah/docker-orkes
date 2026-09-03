# 07 — Git Mirror Backup Sync

**What to build:** An operator can configure an external Git repository as a Git Mirror in the dashboard settings, enabling automatic or manual versioned backups of all Stack compose definitions and Deployment node assignments organized into `/stacks/` and `/deployments/` directories.

**Blocked by:** 04 — Deployment Lifecycle (Deploy, Stop, Restart via SFTP & SSH)

**Status:** resolved

- [x] Operator can configure an external Git repository URL, branch, and credentials in the Control Plane settings.
- [x] Control Plane maintains a local mirror repository directory containing:
  - `stacks/<stack-name>/docker-compose.yml`
  - `stacks/<stack-name>/.env.example`
  - `deployments/<node-name>/<stack-name>.env`
  - `inventory.json` (metadata mapping Nodes, Stacks, and active Deployments)
- [x] Any mutation to Stacks or Deployments triggers an automatic commit and push to the configured Git mirror with descriptive commit messages.
- [x] Operator can click "Sync Now" to manually trigger an immediate push or pull to reconcile Control Plane state with the Git repository.
- [x] Sync failures (e.g. authentication error, network timeout) are recorded in the system status and surfaced with clear error messages without halting Control Plane operations.
- [x] Automated tests verify repository initialization, directory layout generation, commit creation, and sync status updates via the HTTP API boundary.
