# Deployment Pre-flight Checks and Execution Feedback

We decided to require interactive Pre-flight Checks with visual diff inspection and dedicated execution feedback for all mutative Deployment lifecycle actions.

Previously, triggering mutative actions (such as `Up`, `Rollout`, `Reconcile`, and `Down`) executed immediately via a single click and HTTP redirect, causing page flickering, providing no visibility into pending configuration changes, and allowing unintentional deployments when configurations were saved ahead of time.

1. **Mandatory Pre-flight Diff Inspection**:
   Mutative actions (specifically `Up` and `Reconcile`) present an interactive Pre-flight Check dialog showing unified diffs between Desired State in the Control Plane and Actual State on the Node.
2. **Secret Masking by Default**:
   Sensitive environment values within configuration diffs are masked by default (`********`) with an explicit unmask toggle to prevent accidental disclosure during screen sharing or operator review.
3. **Zero-Drift Clarity**:
   When configurations are identical between Control Plane and Node, the Pre-flight Check explicitly indicates zero drift and confirms that executing `Up` will idempotently ensure container readiness via Docker Compose.
4. **Execution Console Feedback**:
   Upon confirmation, the interface transitions directly into an active execution console displaying real-time command output from the SSH Executor, eliminating blind page flickering and surfacing command errors immediately.
5. **Connectivity Safeguards**:
   If a Node cannot be reached during the Pre-flight Check, the deployment action is strictly blocked rather than blindly attempted, preventing partial state corruption.
6. **State-Aware Action Visibility**:
   When a Deployment is in the `stopped` status, non-applicable actions (`Restart Stack` and `Stop Stack (Down)`) are hidden entirely from the interface to avoid redundant or invalid operations on offline workloads.
7. **Unified Lifecycle Action Confirmation**:
   All lifecycle actions (`Up`, `Pull & Rollout`, `Restart Stack`, and `Stop Stack (Down)`) utilize a unified confirmation dialog detailing exact operational scope, configuration effects, and downtime implications prior to execution.
