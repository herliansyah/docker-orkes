package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"docker-orkes/internal/server"
	"docker-orkes/internal/store"
)

type mockDeployExecutor struct {
	mu           sync.Mutex
	uploaded     map[string][]byte
	executedCmds []string
}

func (m *mockDeployExecutor) TestConnection(ctx context.Context, host string, port int, user string, privKey string) (string, error) {
	return "Docker version 27.1.1", nil
}

func (m *mockDeployExecutor) RunCommand(ctx context.Context, host string, port int, user string, privKey string, cmd string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executedCmds = append(m.executedCmds, cmd)
	return "done", nil
}

func (m *mockDeployExecutor) RunCommandStream(ctx context.Context, host string, port int, user string, privKey string, cmd string, out io.Writer) error {
	m.mu.Lock()
	m.executedCmds = append(m.executedCmds, cmd)
	m.mu.Unlock()
	_, _ = out.Write([]byte("Creating network\nContainer web Created\nContainer web Started\n"))
	return nil
}

func (m *mockDeployExecutor) UploadFiles(ctx context.Context, host string, port int, user string, privKey string, remoteDir string, files map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploaded == nil {
		m.uploaded = make(map[string][]byte)
	}
	for k, v := range files {
		m.uploaded[remoteDir+"/"+k] = v
	}
	return nil
}

func (m *mockDeployExecutor) ReadRemoteFile(ctx context.Context, host string, port int, user string, privKey string, path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.uploaded[path], nil
}

func TestServer_DeploymentLifecycle(t *testing.T) {
	mockExec := &mockDeployExecutor{}

	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
		Executor:       mockExec,
	})
	if err != nil {
		t.Fatalf("failed to initialize server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// 1. Authenticate
	loginForm := url.Values{"password": {"secret-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// 2. Create Node
	if err := srv.Store().CreateNode(store.Node{
		ID:     "node-1",
		Name:   "Worker 1",
		Host:   "10.0.0.1",
		Port:   22,
		User:   "root",
		Status: "online",
	}); err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// 3. Create Stack
	composeContent := "services:\n  web:\n    image: nginx:alpine\n"
	if err := srv.Store().CreateStack(store.Stack{
		ID:          "stack-1",
		Name:        "web-proxy",
		ComposeYAML: composeContent,
		DefaultEnv:  "PORT=80",
	}); err != nil {
		t.Fatalf("failed to create stack: %v", err)
	}

	// 4. Create Deployment via POST /deployments
	deployForm := url.Values{
		"stack_id": {"stack-1"},
		"node_id":  {"node-1"},
		"env_vars": {"PORT=8080\nCUSTOM_TAG=v1"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments", strings.NewReader(deployForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after deployment creation, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 5. Verify SFTP files uploaded to ~/.docker-orkes/stacks/web-proxy
	expectedComposePath := ".docker-orkes/stacks/web-proxy/docker-compose.yml"
	expectedEnvPath := ".docker-orkes/stacks/web-proxy/.env"

	if string(mockExec.uploaded[expectedComposePath]) != composeContent {
		t.Errorf("expected uploaded compose to match, got %q", string(mockExec.uploaded[expectedComposePath]))
	}
	if !strings.Contains(string(mockExec.uploaded[expectedEnvPath]), "PORT=8080") {
		t.Errorf("expected uploaded env to contain PORT=8080, got %q", string(mockExec.uploaded[expectedEnvPath]))
	}

	// 6. Verify command execution: docker compose up -d
	var hasUpCmd bool
	for _, cmd := range mockExec.executedCmds {
		if strings.Contains(cmd, "docker compose") && strings.Contains(cmd, "up -d") {
			hasUpCmd = true
			break
		}
	}
	if !hasUpCmd {
		t.Errorf("expected docker compose up -d command, executed: %v", mockExec.executedCmds)
	}

	// 7. Verify Deployment saved and status is 'running'
	deployments, err := srv.Store().ListDeployments()
	if err != nil || len(deployments) != 1 {
		t.Fatalf("expected 1 deployment, got %d (err: %v)", len(deployments), err)
	}
	deployID := deployments[0].ID
	if deployments[0].Status != "running" {
		t.Errorf("expected deployment status 'running', got %q", deployments[0].Status)
	}
	if deployments[0].DesiredHash == "" {
		t.Errorf("expected desired_hash to be populated")
	}

	// 8. Trigger Restart action via POST /deployments/{id}/restart
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/restart", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after restart, got %d", rec.Code)
	}

	// 9. Trigger Down action via POST /deployments/{id}/down
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/down", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after down, got %d", rec.Code)
	}

	depAfterDown, err := srv.Store().GetDeployment(deployID)
	if err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if depAfterDown.Status != "stopped" {
		t.Errorf("expected status 'stopped' after down, got %q", depAfterDown.Status)
	}
}
