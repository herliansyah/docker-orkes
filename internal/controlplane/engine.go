package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
	"docker-orkes/internal/drift"
	"docker-orkes/internal/gitsync"
	"docker-orkes/internal/nodeclient"
	"docker-orkes/internal/ssh"
	"docker-orkes/internal/store"
)

type Config struct {
	Store        *store.Store
	Executor     ssh.Executor
	GitSyncer    gitsync.Syncer
	GitMirrorDir string
	PrivKeyPEM   string
}

type SaveFileResult struct {
	FileName    string `json:"file"`
	Redeploy    bool   `json:"redeploy"`
	RedeployErr string `json:"redeploy_err,omitempty"`
	Output      string `json:"output"`
	DriftStatus string `json:"drift_status"`
}

// Engine encapsulates core Control Plane domain operations.
type Engine interface {
	Store() *store.Store

	// Node operations
	TestNode(ctx context.Context, nodeID string) (dockerVersion string, err error)
	BootstrapNode(ctx context.Context, nodeID string, password string, pubKey string) error
	GetNodeTelemetry(ctx context.Context, nodeID string) (diskUsage string, containers []nodeclient.ContainerInfo, err error)
	GetContainerLogs(ctx context.Context, nodeID string, containerID string) (string, error)

	// Deployment operations
	Deploy(ctx context.Context, deploymentID string) error
	Rollback(ctx context.Context, deploymentID string, revisionID string) error
	ListRevisions(ctx context.Context, deploymentID string) ([]store.DeploymentRevision, error)
	Restart(ctx context.Context, deploymentID string) error
	Down(ctx context.Context, deploymentID string) error
	DeleteDeployment(ctx context.Context, deploymentID string, teardown bool, purgeVolumes bool, force bool) error
	CheckDrift(ctx context.Context, deploymentID string) (*drift.DriftResult, error)
	ReconcileDrift(ctx context.Context, deploymentID string) error
	AdoptDrift(ctx context.Context, deploymentID string) error
	ListDeploymentFiles(ctx context.Context, deploymentID string, subpath ...string) (remoteDir string, files []ssh.RemoteFileEntry, err error)
	ViewDeploymentFile(ctx context.Context, deploymentID string, filename string) (remoteFilePath string, content []byte, err error)
	SaveDeploymentFile(ctx context.Context, deploymentID string, filename string, content []byte, redeploy bool) (*SaveFileResult, error)
	CreateDeploymentDir(ctx context.Context, deploymentID string, dirPath string) error
	DeleteDeploymentPath(ctx context.Context, deploymentID string, path string) error
	ExtractDeploymentArchive(ctx context.Context, deploymentID string, archiveFilename string, targetDir string) error
	UploadDeploymentFiles(ctx context.Context, deploymentID string, files map[string][]byte) error

	// Git Mirror operations
	SyncGitMirror(ctx context.Context) error
	BuildGitMirrorFiles() (map[string][]byte, error)
}

type engine struct {
	cfg Config
}

// New creates a new Control Plane Engine.
func New(cfg Config) Engine {
	if cfg.GitMirrorDir == "" {
		cfg.GitMirrorDir = "data/git-mirror"
	}
	if cfg.Executor == nil {
		cfg.Executor = ssh.NewExecutor()
	}
	if cfg.GitSyncer == nil {
		cfg.GitSyncer = gitsync.NewSyncer()
	}
	return &engine{cfg: cfg}
}

func (e *engine) Store() *store.Store {
	return e.cfg.Store
}

func (e *engine) getNodeClient(node *store.Node) nodeclient.Client {
	return nodeclient.New(nodeclient.Config{
		Host:       node.Host,
		Port:       node.Port,
		User:       node.User,
		PrivKeyPEM: e.cfg.PrivKeyPEM,
		Executor:   e.cfg.Executor,
	})
}

func (e *engine) TestNode(ctx context.Context, nodeID string) (string, error) {
	node, err := e.cfg.Store.GetNode(nodeID)
	if err != nil {
		return "", fmt.Errorf("node not found: %w", err)
	}

	client := e.getNodeClient(node)
	dockerVer, err := client.TestConnection(ctx)
	if err != nil {
		_ = e.cfg.Store.UpdateNodeStatus(node.ID, "offline", err.Error(), "")
		return "", err
	}

	_ = e.cfg.Store.UpdateNodeStatus(node.ID, "online", "", dockerVer)
	return dockerVer, nil
}

func (e *engine) BootstrapNode(ctx context.Context, nodeID string, password string, pubKey string) error {
	node, err := e.cfg.Store.GetNode(nodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	client := e.getNodeClient(node)
	if err := client.BootstrapKey(ctx, password, pubKey); err != nil {
		return fmt.Errorf("failed to inject ssh key: %w", err)
	}

	dockerVer, testErr := client.TestConnection(ctx)
	if testErr != nil {
		_ = e.cfg.Store.UpdateNodeStatus(node.ID, "offline", testErr.Error(), "")
		return fmt.Errorf("ssh key copied, but verification failed: %w", testErr)
	}

	_ = e.cfg.Store.UpdateNodeStatus(node.ID, "online", "", dockerVer)
	return nil
}

func (e *engine) GetNodeTelemetry(ctx context.Context, nodeID string) (string, []nodeclient.ContainerInfo, error) {
	node, err := e.cfg.Store.GetNode(nodeID)
	if err != nil {
		return "", nil, fmt.Errorf("node not found: %w", err)
	}

	client := e.getNodeClient(node)
	return client.GetTelemetry(ctx)
}

func (e *engine) GetContainerLogs(ctx context.Context, nodeID string, containerID string) (string, error) {
	node, err := e.cfg.Store.GetNode(nodeID)
	if err != nil {
		return "", fmt.Errorf("node not found: %w", err)
	}

	client := e.getNodeClient(node)
	return client.GetContainerLogs(ctx, containerID)
}

func (e *engine) Deploy(ctx context.Context, deploymentID string) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	// ponytail: 10 minutes context for pulling large multi-container docker images
	deployCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	files := map[string][]byte{
		"docker-compose.yml": []byte(stack.ComposeYAML),
		".env":               []byte(dep.EnvVars),
	}

	client := e.getNodeClient(node)
	out, err := client.DeployStack(deployCtx, stack.Name, files)
	if err != nil {
		_ = e.cfg.Store.UpdateDeploymentStatus(dep.ID, "failed", err.Error())
		return fmt.Errorf("failed to deploy stack: %w", err)
	}

	hash := drift.ComputeHash(stack.ComposeYAML, dep.EnvVars)
	_ = e.cfg.Store.UpdateDeploymentDesiredHash(dep.ID, hash)
	_ = e.cfg.Store.UpdateDeploymentStatus(dep.ID, "running", out)

	// ponytail: Forward-only revisions bounded to last 10 snapshots (ADR 0005)
	if nextRev, err := e.cfg.Store.GetNextRevisionNumber(dep.ID); err == nil {
		_ = e.cfg.Store.CreateDeploymentRevision(store.DeploymentRevision{
			DeploymentID:   dep.ID,
			RevisionNumber: nextRev,
			ComposeYAML:    stack.ComposeYAML,
			EnvVars:        dep.EnvVars,
			DesiredHash:    hash,
			Reason:         "deploy",
		})
	}

	// Invariant: Git Mirror backup reflects successful deployments
	_ = e.SyncGitMirror(ctx)
	return nil
}

func (e *engine) Rollback(ctx context.Context, deploymentID string, revisionID string) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	rev, err := e.cfg.Store.GetDeploymentRevision(revisionID)
	if err != nil {
		return fmt.Errorf("revision not found: %w", err)
	}
	if rev.DeploymentID != dep.ID {
		return fmt.Errorf("revision does not belong to deployment")
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}

	if stack.ComposeYAML != rev.ComposeYAML {
		stack.ComposeYAML = rev.ComposeYAML
		if err := e.cfg.Store.UpdateStack(*stack); err != nil {
			return fmt.Errorf("failed to revert stack compose: %w", err)
		}
	}

	if dep.EnvVars != rev.EnvVars {
		if err := e.cfg.Store.UpdateDeploymentEnv(dep.ID, rev.EnvVars, dep.Notes); err != nil {
			return fmt.Errorf("failed to revert deployment env: %w", err)
		}
	}

	return e.Deploy(ctx, deploymentID)
}

func (e *engine) ListRevisions(ctx context.Context, deploymentID string) ([]store.DeploymentRevision, error) {
	return e.cfg.Store.ListDeploymentRevisions(deploymentID)
}

func (e *engine) Restart(ctx context.Context, deploymentID string) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	client := e.getNodeClient(node)
	out, err := client.RestartStack(ctx, stack.Name)
	if err != nil {
		_ = e.cfg.Store.UpdateDeploymentStatus(dep.ID, "failed", err.Error())
		return fmt.Errorf("failed to restart stack: %w", err)
	}

	_ = e.cfg.Store.UpdateDeploymentStatus(dep.ID, "running", out)
	return nil
}

func (e *engine) Down(ctx context.Context, deploymentID string) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	client := e.getNodeClient(node)
	out, err := client.DownStack(ctx, stack.Name)
	if err != nil {
		_ = e.cfg.Store.UpdateDeploymentStatus(dep.ID, "failed", err.Error())
		return fmt.Errorf("failed to down stack: %w", err)
	}

	_ = e.cfg.Store.UpdateDeploymentStatus(dep.ID, "stopped", out)
	return nil
}
func (e *engine) DeleteDeployment(ctx context.Context, deploymentID string, teardown bool, purgeVolumes bool, force bool) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	if teardown {
		stackDir := fmt.Sprintf(".docker-orkes/stacks/%s", stack.Name)
		composeFile := fmt.Sprintf("%s/docker-compose.yml", stackDir)
		downCmd := fmt.Sprintf("docker compose -f %s down", composeFile)
		if purgeVolumes {
			downCmd = fmt.Sprintf("docker compose -f %s down -v", composeFile)
		}

		_, sshErr := e.cfg.Executor.RunCommand(ctx, node.Host, node.Port, node.User, e.cfg.PrivKeyPEM, downCmd)
		if sshErr != nil {
			if !force {
				return fmt.Errorf("failed to teardown stack on node: %w", sshErr)
			}
			log.Printf("[WARN] SSH teardown failed for deployment %s on node %s (force=true): %v", deploymentID, node.ID, sshErr)
		} else if purgeVolumes {
			rmCmd := fmt.Sprintf("rm -rf -- %q", stackDir)
			if _, rmErr := e.cfg.Executor.RunCommand(ctx, node.Host, node.Port, node.User, e.cfg.PrivKeyPEM, rmCmd); rmErr != nil {
				if !force {
					return fmt.Errorf("failed to purge stack directory on node: %w", rmErr)
				}
				log.Printf("[WARN] SSH purge stack directory failed for deployment %s on node %s (force=true): %v", deploymentID, node.ID, rmErr)
			}
		}
	}

	if err := e.cfg.Store.DeleteDeployment(deploymentID); err != nil {
		return fmt.Errorf("failed to delete deployment metadata: %w", err)
	}

	_ = e.SyncGitMirror(ctx)
	return nil
}


func (e *engine) CheckDrift(ctx context.Context, deploymentID string) (*drift.DriftResult, error) {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return nil, fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return nil, fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}

	client := e.getNodeClient(node)
	remoteComposeBytes, _ := client.ReadStackFile(ctx, stack.Name, "docker-compose.yml")
	remoteEnvBytes, _ := client.ReadStackFile(ctx, stack.Name, ".env")

	desired := drift.Snapshot{
		ComposeYAML: stack.ComposeYAML,
		EnvVars:     dep.EnvVars,
	}
	actual := drift.Snapshot{
		ComposeYAML: string(remoteComposeBytes),
		EnvVars:     string(remoteEnvBytes),
	}

	eval := drift.Evaluate(desired, actual)
	driftStatus := "synced"
	if eval.IsDrifted {
		driftStatus = "drifted"
	}

	_ = e.cfg.Store.UpdateDeploymentDrift(dep.ID, driftStatus, eval.Diff, actual.ComposeYAML, actual.EnvVars)
	return &eval, nil
}

func (e *engine) ReconcileDrift(ctx context.Context, deploymentID string) error {
	return e.Deploy(ctx, deploymentID)
}

func (e *engine) AdoptDrift(ctx context.Context, deploymentID string) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}

	if dep.RemoteCompose != "" && dep.RemoteCompose != stack.ComposeYAML {
		stack.ComposeYAML = dep.RemoteCompose
		_ = e.cfg.Store.UpdateStack(*stack)
	}

	newCompose := stack.ComposeYAML
	if dep.RemoteCompose != "" {
		newCompose = dep.RemoteCompose
	}

	actualHash := drift.ComputeHash(newCompose, dep.RemoteEnv)
	_ = e.cfg.Store.UpdateDeploymentEnvAndHash(dep.ID, dep.RemoteEnv, actualHash)

	// Invariant: Git Mirror must reflect adopted state
	_ = e.SyncGitMirror(ctx)
	return nil
}

func (e *engine) ListDeploymentFiles(ctx context.Context, deploymentID string, subpath ...string) (string, []ssh.RemoteFileEntry, error) {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return "", nil, fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return "", nil, fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return "", nil, fmt.Errorf("node not found: %w", err)
	}

	sub := ""
	if len(subpath) > 0 && subpath[0] != "" {
		sub = subpath[0]
	}

	remoteDir := fmt.Sprintf(".docker-orkes/stacks/%s", stack.Name)
	if sub != "" {
		remoteDir = fmt.Sprintf(".docker-orkes/stacks/%s/%s", stack.Name, sub)
	}
	client := e.getNodeClient(node)
	files, err := client.ListStackFiles(ctx, stack.Name, sub)
	if err != nil {
		return remoteDir, nil, err
	}
	return remoteDir, files, nil
}

func (e *engine) ViewDeploymentFile(ctx context.Context, deploymentID string, filename string) (string, []byte, error) {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return "", nil, fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return "", nil, fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return "", nil, fmt.Errorf("node not found: %w", err)
	}

	remoteFilePath := fmt.Sprintf(".docker-orkes/stacks/%s/%s", stack.Name, filename)
	client := e.getNodeClient(node)
	content, err := client.ReadStackFile(ctx, stack.Name, filename)
	if err != nil {
		return remoteFilePath, nil, err
	}
	return remoteFilePath, content, nil
}

func (e *engine) SaveDeploymentFile(ctx context.Context, deploymentID string, filename string, content []byte, redeploy bool) (*SaveFileResult, error) {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return nil, fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return nil, fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}

	client := e.getNodeClient(node)
	if err := client.SaveStackFile(ctx, stack.Name, filename, content); err != nil {
		return nil, fmt.Errorf("failed to save remote file: %w", err)
	}

	res := &SaveFileResult{
		FileName: filename,
		Redeploy: redeploy,
	}

	if redeploy {
		cmdCtx, cmdCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cmdCancel()

		out, err := client.UpStack(cmdCtx, stack.Name)
		if err != nil {
			res.RedeployErr = err.Error()
			_ = e.cfg.Store.UpdateDeploymentStatus(dep.ID, "failed", fmt.Sprintf("redeploy error: %v (out: %s)", err, out))
		} else {
			_ = e.cfg.Store.UpdateDeploymentStatus(dep.ID, "running", out)
			res.Output = out
		}
	}

	// Re-check drift
	driftRes, err := e.CheckDrift(ctx, deploymentID)
	if err == nil {
		if driftRes.IsDrifted {
			res.DriftStatus = "drifted"
		} else {
			res.DriftStatus = "synced"
		}
	}

	return res, nil
}

func (e *engine) CreateDeploymentDir(ctx context.Context, deploymentID string, dirPath string) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}
	client := e.getNodeClient(node)
	return client.CreateStackDir(ctx, stack.Name, dirPath)
}

func (e *engine) DeleteDeploymentPath(ctx context.Context, deploymentID string, path string) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}
	client := e.getNodeClient(node)
	return client.DeleteStackPath(ctx, stack.Name, path)
}

func (e *engine) ExtractDeploymentArchive(ctx context.Context, deploymentID string, archiveFilename string, targetDir string) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}
	client := e.getNodeClient(node)
	return client.ExtractStackArchive(ctx, stack.Name, archiveFilename, targetDir)
}

func (e *engine) UploadDeploymentFiles(ctx context.Context, deploymentID string, files map[string][]byte) error {
	dep, err := e.cfg.Store.GetDeployment(deploymentID)
	if err != nil {
		return fmt.Errorf("deployment not found: %w", err)
	}
	stack, err := e.cfg.Store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := e.cfg.Store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}
	client := e.getNodeClient(node)
	return client.UploadStackFiles(ctx, stack.Name, files)
}

func (e *engine) BuildGitMirrorFiles() (map[string][]byte, error) {
	files := make(map[string][]byte)

	stacks, err := e.cfg.Store.ListStacks()
	if err != nil {
		return nil, err
	}
	for _, st := range stacks {
		composePath := fmt.Sprintf("stacks/%s/docker-compose.yml", st.Name)
		files[composePath] = []byte(st.ComposeYAML)

		if st.DefaultEnv != "" {
			envPath := fmt.Sprintf("stacks/%s/.env.example", st.Name)
			files[envPath] = []byte(st.DefaultEnv)
		}
	}

	nodes, err := e.cfg.Store.ListNodes()
	if err != nil {
		return nil, err
	}

	deployments, err := e.cfg.Store.ListDeployments()
	if err != nil {
		return nil, err
	}
	for _, d := range deployments {
		depEnvPath := fmt.Sprintf("deployments/%s/%s.env", d.NodeName, d.StackName)
		files[depEnvPath] = []byte(d.EnvVars)
	}

	runbookNotes, _ := e.cfg.Store.GetSetting("global_runbook_notes")
	if runbookNotes != "" {
		files["RUNBOOK.md"] = []byte(runbookNotes)
	}

	inventory := map[string]interface{}{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"nodes":        nodes,
		"stacks":       stacks,
		"deployments":  deployments,
	}
	invJSON, _ := json.MarshalIndent(inventory, "", "  ")
	files["inventory.json"] = invJSON

	return files, nil
}

func (e *engine) SyncGitMirror(ctx context.Context) error {
	files, err := e.BuildGitMirrorFiles()
	if err != nil {
		_ = e.cfg.Store.SetSetting("git_mirror_status", "failed")
		_ = e.cfg.Store.SetSetting("git_mirror_error", err.Error())
		return err
	}

	repoURL, _ := e.cfg.Store.GetSetting("git_mirror_url")
	branch, _ := e.cfg.Store.GetSetting("git_mirror_branch")
	if branch == "" {
		branch = "main"
	}

	syncErr := e.cfg.GitSyncer.Sync(ctx, e.cfg.GitMirrorDir, repoURL, branch, e.cfg.PrivKeyPEM, files)
	_ = e.cfg.Store.SetSetting("git_mirror_last_sync", time.Now().UTC().Format(time.RFC3339))

	if syncErr != nil {
		_ = e.cfg.Store.SetSetting("git_mirror_status", "failed")
		_ = e.cfg.Store.SetSetting("git_mirror_error", syncErr.Error())
		return syncErr
	}

	_ = e.cfg.Store.SetSetting("git_mirror_status", "synced")
	_ = e.cfg.Store.SetSetting("git_mirror_error", "")
	return nil
}
