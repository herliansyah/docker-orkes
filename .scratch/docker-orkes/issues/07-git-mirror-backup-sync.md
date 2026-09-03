# 07 — Git Mirror Backup Sync

**What to build:** An operator can configure an external Git repository as a Git Mirror in the dashboard settings, enabling automatic or manual versioned backups of all Stack compose definitions and Deployment node assignments organized into `/stacks/` and `/deployments/` directories.

**Blocked by:** 04 — Deployment Lifecycle (Deploy, Stop, Restart via SFTP & SSH)

**Status:** ready-for-agent

- [ ] Settings page allows configuring a Git Mirror repository URL, branch, and authentication credentials (SSH key or Personal Access Token).
- [ ] Control Plane validates connection to the external Git repository upon saving settings.
- [ ] Whenever a Stack template or Deployment is created, edited, or deleted, an asynchronous job stages and commits changes to the Git mirror repository.
- [ ] Stacks are written to `/stacks/<stack-slug>/docker-compose.yml` and `/stacks/<stack-slug>/metadata.json`.
- [ ] Deployments are written to `/deployments/<node-slug>/<stack-slug>.json` recording node associations and non-sensitive metadata.
- [ ] Dashboard displays the last sync timestamp, current commit hash, and sync status badge (Synced, Pending, or Error), with a manual "Sync Now" button.
- [ ] Automated tests verify repository formatting, commit message generation, and synchronization logic via the HTTP API boundary.
