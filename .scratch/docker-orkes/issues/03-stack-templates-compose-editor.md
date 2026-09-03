# 03 — Stack Templates & Compose Editor

**What to build:** A developer can create, view, edit, and delete reusable Stack templates defined by a `docker-compose.yml` file and default environment variable definitions through an in-browser editor with validation.

**Blocked by:** 01 — Server Foundation, Auth, & Keypair Initialization

**Status:** ready-for-agent

- [ ] Developer can create a new Stack by entering a unique name/slug, description, and `docker-compose.yml` body.
- [ ] Developer can define default environment variables (key-value pairs) associated with the Stack template.
- [ ] The compose editor performs client-side or server-side YAML syntax validation before saving, rejecting malformed YAML with clear line errors.
- [ ] Existing Stacks are listed with their metadata, last updated timestamp, and number of active deployments.
- [ ] Developer can view and edit an existing Stack's compose definition and default environment variables.
- [ ] Developer can delete a Stack template (prevented or warned if active Deployments reference it).
- [ ] Automated tests verify Stack creation, YAML validation, and retrieval via the HTTP API boundary.
