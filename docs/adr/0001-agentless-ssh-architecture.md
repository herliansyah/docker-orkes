# Agentless SSH Architecture

We decided to communicate with remote Nodes using agentless SSH rather than installing daemon agents on each Node. Remote nodes only need an active SSH server and a Docker daemon with an authorized SSH key; the Control Plane transfers compose files, executes lifecycle commands (`docker compose`), and collects host metrics via SSH. This eliminates multi-binary distribution and remote agent maintenance overhead, with the accepted trade-off that remote nodes must have reachable SSH endpoints.
