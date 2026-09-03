# 05 — Drift Detection & Visual Side-by-Side Diff

**What to build:** An operator can run a Drift Check on an active Deployment to compare remote file checksums against the Desired State, inspect a visual side-by-side or unified line diff if drift is detected, and choose to reconcile (overwrite remote) or adopt (save remote changes to Control Plane).

**Blocked by:** 04 — Deployment Lifecycle (Deploy, Stop, Restart via SFTP & SSH)

**Status:** resolved

- [x] Control Plane computes and stores SHA-256 digests of the Desired State (`docker-compose.yml` and `.env`) during every successful deployment or update.
- [x] Operator can click "Check Drift" on a Deployment to fetch remote file contents over SFTP/SSH and compute current actual checksums.
- [x] If checksums match, Deployment displays a green `SYNCED` badge with the timestamp of the last check.
- [x] If checksums differ, Deployment displays an amber `DRIFTED` badge and opens a diff viewer highlighting line-by-line additions, deletions, and modifications between Desired State and Actual State.
- [x] Operator can click "Reconcile (Overwrite)" to push the Desired State to the Node, run `docker compose up -d`, and return the Deployment to `SYNCED`.
- [x] Operator can click "Adopt Remote" to update the Control Plane's stored configuration with the remote Node's current files, setting Desired State to match Actual State.
- [x] Automated tests assert correct checksum comparison, diff calculation, reconcile workflows, and adopt workflows via the HTTP API boundary.

