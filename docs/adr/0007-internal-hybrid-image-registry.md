# Internal Hybrid Image Registry Architecture

We decided to support container image distribution across remote Nodes using a hybrid OCI registry powered by Zot deployed as a standard Stack on an infrastructure Node, rather than running heavyweight registry suites (e.g. Harbor) or relying exclusively on upstream public registries.

## Context & Problem
Remote Nodes pull application and utility images repeatedly. Direct pulls from Docker Hub suffer from download rate limits, external bandwidth costs, and network latency. Furthermore, proprietary application images require private storage while maintaining offsite backups on Docker Hub for disaster recovery.

## Decision
1. **Single-Container Hybrid Engine (Zot)**: We use Zot (`project-zot/zot`) as a lightweight (~30MB RAM), single-binary OCI registry container that simultaneously supports local image push/pull and on-demand pull-through caching for Docker Hub. This avoids coordinating multi-container setups (separate cache + push registries behind reverse proxies).
2. **Dual-Push CI/CD Replication**: CI/CD pipelines push proprietary images in parallel to both the internal Image Registry (primary) and Docker Hub (backup). We reject running active bi-directional synchronization daemons or replication workers on the registry host, adhering to our principle of operational simplicity (Ponytail rule: boring over clever).
3. **Transparent Node Integration**: Remote Nodes configure `/etc/docker/daemon.json` with `registry-mirrors` and `insecure-registries` pointing to the internal Image Registry within the private mesh/overlay network. Nodes transparently fetch cached upstream images without modifying existing Stack compose files.
4. **Disaster Recovery**: If the Image Registry Node becomes unavailable, Docker daemons on remote Nodes natively fall back to pulling public images directly from Docker Hub. For proprietary images, operators can temporarily switch deployment references to the Docker Hub backup repository.
