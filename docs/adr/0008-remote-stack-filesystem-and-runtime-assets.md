# Remote Stack Filesystem and Runtime Assets Management

We decided to support full interactive filesystem management (creating files, creating directories, uploading files/directories, and in-place archive extraction) within the remote Stack Directory on a Node via agentless SFTP and native remote CLI utilities, while treating auxiliary non-compose files as unmonitored Runtime Assets.

## Context & Problem
Deployments frequently require auxiliary configuration files, SSL certificates, initial database seed scripts, or static asset directories mounted as volumes alongside `docker-compose.yml`. Previously, operators could only view and edit flat files in the root of the Stack Directory. Uploading directory trees, creating nested configs, or extracting compressed bundles required manual SSH access outside of Docker Orkes.

## Decision
1. **Hierarchical Subdirectory Scoping**: The file explorer supports bidirectional directory navigation with breadcrumbs scoped strictly within the target Stack Directory (`~/.docker-orkes/stacks/<stack-name>/`). Path traversal sequences (`..`) and absolute path roots are strictly rejected to prevent access outside the stack boundary.
2. **Runtime Assets vs. Desired State**: Files and directories other than `docker-compose.yml` and `.env` files are designated as **Runtime Assets**. They reside on the Node's filesystem and are ignored by the background Poller during Drift Checks to keep drift auditing lightweight and avoid hashing gigabytes of static data.
3. **Agentless In-Place Archive Extraction**: Extraction of `.tar.gz`, `.tgz`, `.tar`, and `.zip` archives executes directly on the remote Node via the SSH Executor using native Linux CLI tools (`tar` and `unzip`). This avoids high network bandwidth and memory overhead in the Control Plane.
4. **Directory Structure Preservation on Upload**: Directory uploads utilize HTML5 `webkitdirectory` to upload nested files with relative paths preserved, automatically creating required subdirectories on the Node via SFTP.
5. **Core Lifecycle Protection**: `docker-compose.yml` is protected from accidental deletion through the file explorer to ensure deployment container lifecycles remain manageable via Docker Compose.
