# Node Image Observability and On-Demand Retention Protocol

We decided to support live Node-level container image observability, operator-controlled retention pinning for on-demand utility images, and selective image deletion with hard safety guards, deliberately rejecting bulk automated image pruning to prevent the accidental loss of scheduled maintenance and backup images.

## Context & Problem
Managed Nodes accumulate Docker container images over time, often contributing to critical host disk pressure. While operators require visibility to audit and reclaim storage, native automated bulk pruning (`docker image prune -a`) creates an acute operational hazard: numerous essential images (such as `percona-xtrabackup`, schema migration runners, and emergency diagnostic toolkits) run only intermittently on cron schedules or ad-hoc operator commands. If these images are wiped during routine disk cleanup, scheduled backups fail or require sudden registry re-pulls that fail in air-gapped environments.

## Decision
1. **Node-Scoped Live Telemetry**: Image inventories are inspected live on-demand via the agentless SSH Executor (`docker images --format '{{json .}}'`) when an operator views a Node's observability view. Telemetry fetching is isolated to a dedicated "Images" tab on the Node Detail page (`/nodes/{id}`) to eliminate unnecessary SSH polling overhead.
2. **Automatic Reference Taxonomy**: The Control Plane cross-references live Docker images against `docker ps -a` container states to classify images into four automated states:
   - `In Use (Running)`: Bound to an active container.
   - `In Use (Stopped)`: Bound to an existing exited or stopped container.
   - `Dangling`: Untagged build layer artifacts (`<none>:<none>`).
   - `Unused`: No local container currently instantiated from the image.
3. **Explicit Operator Pinning (`On-Demand Image`)**: Operators can mark any tagged image as an **On-Demand Image** via `POST /nodes/{id}/images/pin`. This relationship is recorded in the Control Plane database (`node_pinned_images`). Pinned images are badged distinctly in the UI to convey intentional retention for maintenance and backup workflows.
4. **Selective Deletion with Hard Block Guards**: Automated bulk prune actions (`docker image prune -a`) are strictly prohibited in the system interface. Image deletion is conducted solely on a per-image basis (`docker rmi`). The system enforces immutable deletion guards:
   - Images in `In Use (Running)` or `In Use (Stopped)` state cannot be deleted; the delete action is disabled in the UI and rejected with HTTP 400 at the API level.
   - Images pinned as `On-Demand Image` have their delete action locked until the operator deliberately unpins the image.
   - Unreferenced and Dangling images can be removed selectively after an explicit confirmation prompt.
5. **Dangling Layer Remediation**: Dangling images (`<none>:<none>`) are explicitly highlighted in the inventory table and can be removed by their unique image ID to clean up orphaned build layers without endangering tagged production or maintenance images.
