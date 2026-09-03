# 06 — Node Observability (Disk Space & Running Containers)

**What to build:** An operator can open a Node's detail screen to observe real-time host disk storage metrics (used, available, percentage bar via `df -h`), view an inventory of active and stopped containers with CPU/memory usage, and open a log viewer to inspect streaming container logs over SSH.

**Blocked by:** 02 — Node Inventory & SSH Connectivity Test

**Status:** resolved

- [x] Navigating to a Node detail page opens an active telemetry stream via Server-Sent Events (SSE).
- [x] Control Plane executes `df -h /` and Docker container inspection commands over SSH to sample disk capacity and container runtime vitals.
- [x] UI renders a visual progress/gauge bar showing disk total capacity, used gigabytes, and remaining free space percentage.
- [x] UI lists all containers on the Node with their container name, image, status (`running`, `exited`), ports, and uptime.
- [x] Clicking on a container opens a log stream drawer/modal that runs `docker logs --tail 100 -f <container_id>` over SSH and streams the output in real time.
- [x] Telemetry stream automatically closes and stops SSH polling when the user navigates away from the Node detail page.
- [x] Automated tests verify telemetry command parsing and SSE payload generation via the HTTP API boundary.

