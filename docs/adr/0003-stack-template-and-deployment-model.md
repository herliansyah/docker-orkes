# Stack Template and Deployment Model

We decided to decouple Stack definitions from their target Nodes by adopting a 1-to-Many template model. A Stack defines the reusable `docker-compose.yml` template and default environment keys, while a Deployment binds a Stack to a specific Node with resolved node-specific environment variables (`.env`). This enables deploying identical compose stacks (e.g. proxy, monitor, workers) across multiple nodes without duplicating compose files, while isolating state and drift checks per Node.
