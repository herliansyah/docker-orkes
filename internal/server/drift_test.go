package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"docker-orkes/internal/server"
	"docker-orkes/internal/store"
)


func TestServer_DriftDetectionReconcileAndAdopt(t *testing.T) {
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

	// 2. Setup Node and Stack
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-drift",
		Name:   "Drift Test Node",
		Host:   "10.0.0.99",
		Port:   22,
		User:   "root",
		Status: "online",
	})
	originalCompose := "services:\n  app:\n    image: redis:7-alpine\n"
	_ = srv.Store().CreateStack(store.Stack{
		ID:          "stack-drift",
		Name:        "redis-stack",
		ComposeYAML: originalCompose,
		DefaultEnv:  "MAX_MEMORY=256mb",
	})

	// 3. Deploy
	deployForm := url.Values{
		"stack_id": {"stack-drift"},
		"node_id":  {"node-drift"},
		"env_vars": {"MAX_MEMORY=256mb"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments", strings.NewReader(deployForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	deps, _ := srv.Store().ListDeployments()
	if len(deps) == 0 {
		t.Fatalf("deployment creation failed")
	}
	depID := deps[0].ID

	// 4. Initial Check Drift -> should be SYNCED
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+depID+"/drift/check", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	dep, err := srv.Store().GetDeployment(depID)
	if err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if dep.DriftStatus != "synced" {
		t.Errorf("expected drift status 'synced', got %q", dep.DriftStatus)
	}

	// 5. Simulate manual modification on remote Node (hotfix directly in server)
	modifiedCompose := "services:\n  app:\n    image: redis:7.2-alpine\n    restart: always\n"
	mockExec.uploaded[".docker-orkes/stacks/redis-stack/docker-compose.yml"] = []byte(modifiedCompose)

	// 6. Check Drift again -> should detect DRIFTED and generate diff
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+depID+"/drift/check", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	dep, err = srv.Store().GetDeployment(depID)
	if err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if dep.DriftStatus != "drifted" {
		t.Errorf("expected drift status 'drifted', got %q", dep.DriftStatus)
	}
	if !strings.Contains(dep.DiffOutput, "redis:7.2-alpine") {
		t.Errorf("expected diff to show modified image, got: %s", dep.DiffOutput)
	}

	// 7. Test "Adopt Remote": update Control Plane to match remote changes
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+depID+"/drift/adopt", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	dep, err = srv.Store().GetDeployment(depID)
	if err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if dep.DriftStatus != "synced" {
		t.Errorf("expected drift status 'synced' after adopt, got %q", dep.DriftStatus)
	}

	stack, err := srv.Store().GetStack("stack-drift")
	if err != nil {
		t.Fatalf("failed to get stack: %v", err)
	}
	if !strings.Contains(stack.ComposeYAML, "redis:7.2-alpine") {
		t.Errorf("expected adopted stack compose to contain redis:7.2-alpine, got %s", stack.ComposeYAML)
	}

	// 8. Introduce drift again, and test "Reconcile (Overwrite)"
	mockExec.uploaded[".docker-orkes/stacks/redis-stack/docker-compose.yml"] = []byte("services:\n  app:\n    image: corrupted:tag\n")
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+depID+"/drift/check", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	req = httptest.NewRequest(http.MethodPost, "/deployments/"+depID+"/drift/reconcile", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// After reconcile, remote file should be restored to Desired State
	remoteFile := string(mockExec.uploaded[".docker-orkes/stacks/redis-stack/docker-compose.yml"])
	if !strings.Contains(remoteFile, "redis:7.2-alpine") {
		t.Errorf("expected remote file to be reconciled to redis:7.2-alpine, got %s", remoteFile)
	}
}
