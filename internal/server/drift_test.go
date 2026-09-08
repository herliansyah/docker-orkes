package server_test

import (
	"encoding/json"
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

func TestServer_CheckDriftJSON(t *testing.T) {
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

	loginForm := url.Values{"password": {"secret-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// Seed node and stack
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-json",
		Name:   "JSON Node",
		Host:   "1.2.3.4",
		Port:   22,
		Status: "online",
	})
	_ = srv.Store().CreateStack(store.Stack{
		ID:          "stack-json",
		Name:        "json-stack",
		ComposeYAML: "services:\n  app:\n    image: nginx:alpine\n",
	})

	// Deploy stack
	form := url.Values{
		"stack_id": {"stack-json"},
		"node_id":  {"node-json"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	deps, err := srv.Store().ListDeployments()
	if err != nil || len(deps) == 0 {
		t.Fatalf("failed to list deployments: %v", err)
	}
	depID := deps[0].ID

	// 1. Initial check drift with Accept: application/json -> in sync
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+depID+"/drift/check", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var res map[string]interface{}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}
	if res["success"] != true {
		t.Errorf("expected success: true, got %v", res["success"])
	}
	if res["is_drifted"] != false {
		t.Errorf("expected is_drifted: false, got %v", res["is_drifted"])
	}
	if res["drift_status"] != "synced" {
		t.Errorf("expected drift_status 'synced', got %v", res["drift_status"])
	}

	// 2. Introduce drift
	mockExec.uploaded[".docker-orkes/stacks/json-stack/docker-compose.yml"] = []byte("services:\n  app:\n    image: nginx:1.25\n")

	req = httptest.NewRequest(http.MethodPost, "/deployments/"+depID+"/drift/check", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	res = nil
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}
	if res["success"] != true {
		t.Errorf("expected success: true, got %v", res["success"])
	}
	if res["is_drifted"] != true {
		t.Errorf("expected is_drifted: true, got %v", res["is_drifted"])
	}
	if res["drift_status"] != "drifted" {
		t.Errorf("expected drift_status 'drifted', got %v", res["drift_status"])
	}
	diff, ok := res["diff"].(string)
	if !ok || !strings.Contains(diff, "nginx:1.25") {
		t.Errorf("expected diff to contain nginx:1.25, got %v", res["diff"])
	}

	// 3. Check drift on non-existent deployment -> error JSON
	req = httptest.NewRequest(http.MethodPost, "/deployments/nonexistent/drift/check", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500 for missing deployment, got %d", rec.Code)
	}
	res = nil
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}
	if res["success"] != false {
		t.Errorf("expected success: false, got %v", res["success"])
	}
}
