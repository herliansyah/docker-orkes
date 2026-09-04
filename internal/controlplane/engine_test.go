package controlplane_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"docker-orkes/internal/controlplane"
	"docker-orkes/internal/ssh"
	"docker-orkes/internal/store"
)

type mockExecutor struct {
	mu           sync.Mutex
	commands     []string
	uploaded     map[string]map[string][]byte
}

func (m *mockExecutor) TestConnection(ctx context.Context, host string, port int, user string, privKey string) (string, error) {
	return "Docker version 27.1.1", nil
}

func (m *mockExecutor) RunCommand(ctx context.Context, host string, port int, user string, privKey string, cmd string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands = append(m.commands, cmd)
	if strings.Contains(cmd, "df -h") {
		return "Filesystem Size Used Avail Use% Mounted on\n/dev/sda1 50G 20G 30G 40% /", nil
	}
	if strings.Contains(cmd, "docker ps") {
		return `{"ID":"c1","Names":"web","Image":"nginx","Status":"Up","State":"running"}` + "\n", nil
	}
	return "done", nil
}

func (m *mockExecutor) RunCommandStream(ctx context.Context, host string, port int, user string, privKey string, cmd string, out io.Writer) error {
	m.mu.Lock()
	m.commands = append(m.commands, cmd)
	m.mu.Unlock()
	return nil
}

func (m *mockExecutor) UploadFiles(ctx context.Context, host string, port int, user string, privKey string, remoteDir string, files map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploaded == nil {
		m.uploaded = make(map[string]map[string][]byte)
	}
	if m.uploaded[remoteDir] == nil {
		m.uploaded[remoteDir] = make(map[string][]byte)
	}
	for k, v := range files {
		m.uploaded[remoteDir][k] = v
	}
	return nil
}

func (m *mockExecutor) ReadRemoteFile(ctx context.Context, host string, port int, user string, privKey string, remoteFilePath string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for dir, files := range m.uploaded {
		for f, content := range files {
			if dir+"/"+f == remoteFilePath {
				return content, nil
			}
		}
	}
	return nil, nil
}

func (m *mockExecutor) ListRemoteFiles(ctx context.Context, host string, port int, user string, privKey string, remoteDir string) ([]ssh.RemoteFileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []ssh.RemoteFileEntry
	if files, ok := m.uploaded[remoteDir]; ok {
		for name, content := range files {
			entries = append(entries, ssh.RemoteFileEntry{
				Name: name,
				Size: int64(len(content)),
			})
		}
	}
	return entries, nil
}

func (m *mockExecutor) BootstrapKey(ctx context.Context, host string, port int, user string, password string, pubKey string) error {
	return nil
}

type mockGitSyncer struct {
	mu          sync.Mutex
	syncedFiles map[string][]byte
}

func (m *mockGitSyncer) Sync(ctx context.Context, mirrorDir string, repoURL string, branch string, privKey string, files map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncedFiles = make(map[string][]byte)
	for k, v := range files {
		m.syncedFiles[k] = v
	}
	return nil
}

func setupTestEngine(t *testing.T) (controlplane.Engine, *mockExecutor, *mockGitSyncer) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	mockExec := &mockExecutor{}
	mockGit := &mockGitSyncer{}

	eng := controlplane.New(controlplane.Config{
		Store:        st,
		Executor:     mockExec,
		GitSyncer:    mockGit,
		GitMirrorDir: "/tmp/test-mirror",
		PrivKeyPEM:   "test-key",
	})

	return eng, mockExec, mockGit
}

func TestControlPlane_DeploymentLifecycle(t *testing.T) {
	eng, mockExec, mockGit := setupTestEngine(t)
	st := eng.Store()
	defer st.Close()

	ctx := context.Background()

	// 1. Create Node & Stack
	if err := st.CreateNode(store.Node{ID: "n1", Name: "worker-1", Host: "10.0.0.1", Port: 22, User: "root"}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.CreateStack(store.Stack{ID: "s1", Name: "web-proxy", ComposeYAML: "services:\n  nginx:\n    image: nginx\n", DefaultEnv: "PORT=80"}); err != nil {
		t.Fatalf("create stack: %v", err)
	}
	if err := st.CreateDeployment(store.Deployment{ID: "d1", StackID: "s1", NodeID: "n1", EnvVars: "PORT=8080"}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}

	// 2. Deploy
	if err := eng.Deploy(ctx, "d1"); err != nil {
		t.Fatalf("Deploy failed: %v", err)
	}

	dep, err := st.GetDeployment("d1")
	if err != nil || dep.Status != "running" {
		t.Fatalf("expected running status, got %q (err: %v)", dep.Status, err)
	}
	if dep.DesiredHash == "" {
		t.Errorf("expected non-empty desired hash")
	}

	// Verify Git mirror was synced automatically
	mockGit.mu.Lock()
	if mockGit.syncedFiles == nil || len(mockGit.syncedFiles) == 0 {
		t.Errorf("expected Git mirror files to be synced on deploy")
	}
	mockGit.mu.Unlock()

	// 3. Restart
	if err := eng.Restart(ctx, "d1"); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}

	// 4. Down
	if err := eng.Down(ctx, "d1"); err != nil {
		t.Fatalf("Down failed: %v", err)
	}
	dep, _ = st.GetDeployment("d1")
	if dep.Status != "stopped" {
		t.Errorf("expected stopped status, got %q", dep.Status)
	}

	// 5. Verify executor received commands
	mockExec.mu.Lock()
	hasDown := false
	for _, cmd := range mockExec.commands {
		if strings.Contains(cmd, "down") {
			hasDown = true
			break
		}
	}
	mockExec.mu.Unlock()
	if !hasDown {
		t.Errorf("expected docker compose down in executor commands")
	}
}

func TestControlPlane_DriftWorkflow(t *testing.T) {
	eng, mockExec, _ := setupTestEngine(t)
	st := eng.Store()
	defer st.Close()

	ctx := context.Background()

	_ = st.CreateNode(store.Node{ID: "n1", Name: "worker-1", Host: "10.0.0.1", Port: 22, User: "root"})
	_ = st.CreateStack(store.Stack{ID: "s1", Name: "app", ComposeYAML: "version: '3'\nservices:\n  app:\n    image: v1\n"})
	_ = st.CreateDeployment(store.Deployment{ID: "d1", StackID: "s1", NodeID: "n1", EnvVars: "APP_ENV=prod"})

	// Deploy initially
	if err := eng.Deploy(ctx, "d1"); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Check drift initially -> should be synced
	eval, err := eng.CheckDrift(ctx, "d1")
	if err != nil {
		t.Fatalf("CheckDrift: %v", err)
	}
	if eval.IsDrifted {
		t.Errorf("expected not drifted, got drifted")
	}

	// Mutate actual remote file on node
	mockExec.mu.Lock()
	mockExec.uploaded[".docker-orkes/stacks/app"][".env"] = []byte("APP_ENV=staging")
	mockExec.mu.Unlock()

	// Check drift again -> should be drifted
	eval, err = eng.CheckDrift(ctx, "d1")
	if err != nil {
		t.Fatalf("CheckDrift 2: %v", err)
	}
	if !eval.IsDrifted {
		t.Errorf("expected drift detected, got false")
	}
	if !strings.Contains(eval.Diff, "APP_ENV=staging") {
		t.Errorf("expected diff to contain APP_ENV=staging, got: %s", eval.Diff)
	}

	// Adopt drift
	if err := eng.AdoptDrift(ctx, "d1"); err != nil {
		t.Fatalf("AdoptDrift: %v", err)
	}

	dep, _ := st.GetDeployment("d1")
	if dep.EnvVars != "APP_ENV=staging" {
		t.Errorf("expected adopted env vars APP_ENV=staging, got %q", dep.EnvVars)
	}
}

func TestControlPlane_TelemetryAndFiles(t *testing.T) {
	eng, _, _ := setupTestEngine(t)
	st := eng.Store()
	defer st.Close()

	ctx := context.Background()

	_ = st.CreateNode(store.Node{ID: "n1", Name: "worker-1", Host: "10.0.0.1", Port: 22, User: "root"})
	_ = st.CreateStack(store.Stack{ID: "s1", Name: "app", ComposeYAML: "services:\n  app:\n    image: nginx\n"})
	_ = st.CreateDeployment(store.Deployment{ID: "d1", StackID: "s1", NodeID: "n1"})

	// Test connection
	ver, err := eng.TestNode(ctx, "n1")
	if err != nil || ver != "Docker version 27.1.1" {
		t.Errorf("TestNode unexpected result: %v (err: %v)", ver, err)
	}

	// Telemetry
	disk, containers, err := eng.GetNodeTelemetry(ctx, "n1")
	if err != nil {
		t.Fatalf("GetNodeTelemetry: %v", err)
	}
	if disk != "40%" {
		t.Errorf("expected disk 40%%, got %q", disk)
	}
	if len(containers) != 1 || containers[0].Names != "web" {
		t.Errorf("unexpected containers: %+v", containers)
	}

	// Save and View file
	saveRes, err := eng.SaveDeploymentFile(ctx, "d1", "custom.conf", []byte("proxy_pass http://backend;"), false)
	if err != nil {
		t.Fatalf("SaveDeploymentFile: %v", err)
	}
	if saveRes.FileName != "custom.conf" {
		t.Errorf("unexpected save result: %+v", saveRes)
	}

	_, content, err := eng.ViewDeploymentFile(ctx, "d1", "custom.conf")
	if err != nil {
		t.Fatalf("ViewDeploymentFile: %v", err)
	}
	if string(content) != "proxy_pass http://backend;" {
		t.Errorf("unexpected file content: %s", string(content))
	}
}
