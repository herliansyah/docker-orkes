# 01 — Server Foundation, Auth, & Keypair Initialization

**What to build:** An operator can launch the single-binary Control Plane, log into the Web Dashboard using a configured master password, and view an automatically generated Ed25519 Control Plane SSH public key in the settings/dashboard so that it can be authorized on target remote servers.

**Blocked by:** None — can start immediately.

**Status:** resolved

- [x] Control Plane launches as a standalone HTTP server and initializes an embedded SQLite database schema on startup.
- [x] On first boot, Control Plane generates a dedicated Ed25519 SSH keypair if one does not already exist, persisting it securely in storage.
- [x] Web Dashboard requires authentication via master password session cookie before any protected routes or actions can be accessed.
- [x] Unauthenticated requests redirect to a clean login screen; invalid passwords show a clear error message.
- [x] Dashboard displays the Control Plane's SSH public key with an interactive "Copy to Clipboard" button and clear instructions for pasting into remote `~/.ssh/authorized_keys`.
- [x] Automated tests verify database bootstrap, keypair generation idempotency, and session authentication via the HTTP boundary.

