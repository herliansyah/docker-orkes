# Node Volumes and Networks Observability and Lifecycle Management

We decided to add on-demand observability and selective lifecycle management (status inspection, system resource protection, single deletion, and batch prune) for Docker Volumes and Networks within the Node detail dashboard via agentless SSH.

## Context & Problem
While operators can observe containers and local OCI images on a Node, Docker daemon storage and network configurations often accumulate dangling volumes and orphan networks from deleted or updated Compose stacks. Operators previously had to log in via SSH to inspect volume disk usage or clean up unused bridge networks.

## Decision
1. **On-Demand Lazy Loading**: Volume and network inventories are queried via SSH (`docker volume ls`, `docker network ls`) only when their respective tabs are activated in the Node detail dashboard, keeping periodic background polling lightweight.
2. **Managed vs. Unmanaged Categorization**:
   - Resources created by Docker Compose (`com.docker.compose.project` label) are flagged as **Managed** and linked to their parent Stack.
   - Resources created manually or by external services are flagged as **Unmanaged**.
3. **Built-in System Protection**:
   - Default daemon networks (`bridge`, `host`, `none`) are designated as **System Networks** and protected against deletion.
   - Volumes and networks actively attached to running or stopped containers are marked as **In-Use** and cannot be deleted.
4. **Targeted Cleanup & Pruning**:
   - Unused volumes and networks support individual removal with confirmation dialogs.
   - Optional batch prune (`docker volume prune -f`, `docker network prune -f`) is provided with explicit data loss warnings.
