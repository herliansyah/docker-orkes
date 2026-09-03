# Control Plane Technology Stack

We decided to implement the Control Plane as a single static Go binary with embedded SQLite (pure Go, CGO-free) and embedded Web UI assets (`go:embed`). This choice provides native concurrency for managing multiple SSH connections, zero external runtime dependencies, minimal memory footprint (~15-30MB RAM), and effortless deployment as a standalone binary or lightweight container.
