# Agentless Transport Security and Filesystem Isolation

We decided to enforce strict parameter escaping and whitelisting at API boundaries, migrate remote Stack Directory filesystem mutations to native SFTP, require strict Stack Slugs, and enforce Origin validation on Web Terminal connections.

## Context & Problem
Interactions with remote Nodes execute over agentless SSH using standard CLI commands and SFTP file transfers. Unvalidated container identifiers in log queries, free-form stack names, and shell-interpolated directory operations (`mkdir -p`, `rm -rf`, and inline Python zip extraction) created command injection risks on remote Nodes. Additionally, unvalidated WebSocket origins exposed Web Terminal sessions to cross-site hijacking, and database sessions lacked time-bounded expiration.

## Decision
1. **Trust Boundary Parameter Whitelisting**: All dynamic identifiers passed into remote Docker CLI invocations (`container_id`, `image_ref`, volume names, network names) are validated against strict alphanumeric whitelists (`^[a-zA-Z0-9_.-]+$`) at the HTTP handler layer.
2. **POSIX Argument Escaping**: Dynamic values passed to the SSH Executor are wrapped with POSIX single-quote escaping to prevent shell interpretation of metacharacters (`$()`, backticks, semicolons).
3. **Canonical Stack Slugs**: Stack names are restricted to URL-safe alphanumeric slugs (`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`) to guarantee safe path resolution under `~/.docker-orkes/stacks/<stack-name>/`.
4. **SFTP-Native Directory Management**: Creating directories and deleting paths within Stack Directories use native SFTP operations (`sftp.Client.MkdirAll` and post-order recursive traversal for deletions) instead of invoking remote shell binaries (`mkdir` and `rm`).
5. **Direct Native Archive Extraction**: Archive extraction is limited to standard Node CLI tools (`tar` and `unzip`) without executing inline Python fallback scripts.
6. **Git Mirror Parameter Isolation**: The `branch` parameter is strictly validated (`^[a-zA-Z0-9_.-]+$`, disallowing leading dashes), remote repository URLs must match allowed schemes (`https://`, `http://`, `ssh://`, `git@`), and Git CLI invocations use `--` separators to prevent option injection.
7. **Time-Bounded Operator Sessions**: Sessions stored in SQLite enforce an explicit 7-day time-to-live verified on every request.
8. **Strict WebSocket Origin Verification**: The Web Terminal WebSocket handshake validates that the incoming `Origin` header matches the request `Host`.
9. **Non-Destructive Legacy Stack Handling**: Strict Stack Slug validation applies to creation and update write operations, preserving existing database records until an operator edits them.
