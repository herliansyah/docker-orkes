# Deployment Deletion Lifecycle and Adoption Safety

We decided to provide an interactive, non-destructive default lifecycle for Deployment Deletion, requiring explicit safety confirmation for data purging and supporting in-place convergence for re-adoption.

When deleting an active Deployment from the Control Plane:
1. **Interactive Teardown vs. Detach**: Operators choose between tearing down containers on the Node (`docker compose down`) or detaching the Deployment. Detaching leaves containers running as Unmanaged Containers and preserves Stack Directories and Node Volumes without downtime.
2. **Protected Data Purge**: Deletion retains Node Volumes and Stack Directory data by default. Purging volumes (`docker compose down -v`) and removing files requires an unchecked option and typed confirmation.
3. **Graceful Offline Handling**: If a Node is unreachable, the Control Plane permits a Force Deletion to remove metadata while logging an audit record.
4. **Audit and Alert Suppression**: Deletions commit file removals to the Git Mirror while retaining historical Revision records in the database marked as `DELETED`. Downtime alerts are muted during intentional teardowns.
5. **In-Place Adoption**: Re-deploying a detached or existing stack uses In-Place Convergence (`docker compose up -d --no-recreate`), preserving matching running containers and existing data volumes without unnecessary restarts.

This prevents catastrophic data loss on stateful workloads (such as database containers), gives operators operational flexibility during node outages, and provides a deterministic path to re-attach unmanaged workloads.
