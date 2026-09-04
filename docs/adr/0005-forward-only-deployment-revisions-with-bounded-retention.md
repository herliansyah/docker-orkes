# Forward-Only Deployment Revisions with Bounded Retention

We decided to record deployment history using immutable, forward-only Revisions with bounded retention (maximum 10 per Deployment in SQLite), rather than mutating existing records or pointing backward.

Whenever a Deployment is deployed or rolled back, the Control Plane captures a complete snapshot of the Desired State (compose template, resolved environment variables, and SHA-256 desired hash) as a new incrementing revision. Rolling back to an earlier revision (e.g. reverting to `v1`) creates a new revision (`v3`) with identical configuration and triggers a deploy.

This preserves an unbroken, auditable progression of states that mirrors the Git Mirror backup timeline, guarantees that rollbacks can themselves be audited, and bounds database growth without operator intervention.
