# Self-Contained Enterprise Web UI and Styling Architecture

We decided to structure the Control Plane Web UI as a responsive, enterprise-grade dark interface with zero external CDN dependencies, embedding all styling and client assets directly into the Go binary (`//go:embed`), and organizing navigation around a collapsible sidebar and hierarchical views.

## Context & Problem
The Control Plane web interface previously relied on an external CDN script (`https://cdn.tailwindcss.com`) for CSS utility rendering. This created critical issues:
1. **Air-gapped & Offline Invariant**: In restricted enterprise networks or air-gapped environments without public internet access, UI rendering degraded or broke entirely.
2. **Security & Privacy**: CDN assets introduce third-party request tracking and external supply-chain attack surfaces.
3. **Information Density & Workflow**: As features expand (Topology Map, Node Observability, Deployments, Runtime Assets, Drift Inspection), a simple horizontal top navigation bar becomes cluttered, making cluster-wide monitoring difficult.

## Decision
1. **Self-Contained Embedded CSS**: All CSS utility rules and design system tokens are bundled into static CSS files inside `internal/server/templates/static/` and embedded directly into the binary (`//go:embed templates/static/*`). Zero requests are made to external CDNs.
2. **Enterprise Dark Design System**: The interface standardizes on an enterprise dark palette (zinc/slate neutrals with high-contrast emerald/amber/rose status indicators and indigo accents), high-contrast borders, and native monospace styling for terminal and diff inspection.
3. **Native High-Quality System Font Stack**: Typography utilizes the client operating system's native sans-serif font stack (`system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif`) and monospace stack, ensuring zero network overhead and zero layout shifts.
4. **Collapsible Sidebar Navigation**: The primary layout uses a left-hand navigation sidebar supporting quick switching between core domain spaces (Topology Map, Nodes, Stacks, Deployments, Images, Settings).
5. **Hybrid Node Overview**: Node inventory provides a toggleable view between rich metric cards (for visual health glances) and compact data tables (for large-scale inventory management).
