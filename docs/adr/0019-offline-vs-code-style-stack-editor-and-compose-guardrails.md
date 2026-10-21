# Offline VS Code-Style Stack Editor and Compose Validation Guardrails

We decided to upgrade the Stack Template Editor in Docker Orkes into a self-contained, VS Code Dark-themed code editor with fullscreen workspace expansion and instant dual-layer Compose validation guardrails, adhering strictly to the air-gapped zero-CDN requirement of [ADR 0011](0011-self-contained-enterprise-ui.md).

## Context & Problem
1. **Ergonomic Limitations of Plain Textareas**: Editing multi-service `docker-compose.yml` templates and environment variables inside standard HTML `<textarea>` elements lacked syntax highlighting, line numbers, automatic 2-space tab indentation, and bracket matching. This made authoring and troubleshooting complex deployment manifests error-prone.
2. **Fixed Viewport Constrictions**: The stack template modal was constrained to a fixed container size (`max-w-4xl`, `max-h-[90vh]`), severely limiting visibility when inspecting or editing large stacks with tens of services, volume mappings, and environment definitions.
3. **Risk of Storing Malformed Compose YAML**: Without pre-save validation, operators could inadvertently save invalid YAML syntax (e.g. invalid tab characters, indentation mismatches) or compose files lacking required `services:` root blocks. When later deployed, these broken stacks triggered runtime failures during remote SSH execution.

## Decision
1. **Self-Contained Embedded CodeMirror 5 Bundle**: Vendor a lightweight, offline-bundled CodeMirror 5 distribution into `internal/server/templates/static/editor.js` and `internal/server/templates/static/editor.css` (~220 KB combined). This bundles core editing capabilities, YAML syntax parsing, properties/shell mode, bracket matching, auto-closing quotes/brackets, and a custom VS Code Dark color palette matching Docker Orkes's `zinc-950`/`indigo-500` enterprise theme. Zero external CDN calls are made.
2. **Fullscreen Workspace Expansion (Maximize Toggle)**: Equip the Stack Template Editor modal header with a toggleable maximize button (`⛶`/`🗖`) and keyboard shortcut (`Esc`). Expanding transitions the editor into a full-screen viewport (`100vw` x `100vh`), allowing the editor canvas to scale 100% vertically and horizontally.
3. **Instant Client-Side Validation Guardrail**: Intercept form submissions before dispatch. Using embedded `js-yaml`, the editor verifies that the compose content is syntactically well-formed and contains a valid `services:` mapping. If errors are detected:
   - Form submission is aborted instantly, preventing data loss or page reload.
   - A prominent error banner details the exact issue.
   - If line coordinates are reported, the faulty line is visually highlighted in red (`cm-error-line`) and the cursor jumps directly to the error position.
4. **Server-Side Enforcement**: In `internal/server/server.go`, enforce `validateComposeYAML` on both `handleCreateStack` and `handleUpdateStack`, verifying both YAML syntax parsing via `gopkg.in/yaml.v3` and the existence of a non-empty `services` mapping, rejecting invalid submissions with HTTP 400 Bad Request.
