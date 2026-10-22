package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"docker-orkes/internal/server"
	"docker-orkes/internal/store"
)

type mockGitSyncer struct {
	mu          sync.Mutex
	syncedFiles map[string][]byte
	syncCount   int
	lastError   error
	repoURL     string
	branch      string
}

func (m *mockGitSyncer) Sync(ctx context.Context, mirrorDir string, repoURL string, branch string, privKey string, files map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncCount++
	m.repoURL = repoURL
	m.branch = branch
	m.syncedFiles = make(map[string][]byte)
	for k, v := range files {
		m.syncedFiles[k] = v
	}
	return m.lastError
}

func TestServer_GitMirrorBackupSync(t *testing.T) {
	mockExec := &mockDeployExecutor{}
	mockGit := &mockGitSyncer{}

	tempDir, err := os.MkdirTemp("", "git-mirror-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
		Executor:       mockExec,
		GitSyncer:      mockGit,
		GitMirrorDir:   filepath.Join(tempDir, "mirror"),
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

	// 2. Configure Git Mirror Settings
	settingsForm := url.Values{
		"git_mirror_url":    {"git@github.com:example/infra-backup.git"},
		"git_mirror_branch": {"main"},
	}
	req = httptest.NewRequest(http.MethodPost, "/settings/git", strings.NewReader(settingsForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after updating git settings, got %d", rec.Code)
	}

	// 3. Create Stack and Deployment to generate inventory
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-git",
		Name:   "Prod-Node-1",
		Host:   "10.0.0.12",
		Port:   22,
		User:   "admin",
		Status: "online",
	})
	composeContent := "services:\n  app:\n    image: postgres:16\n"
	_ = srv.Store().CreateStack(store.Stack{
		ID:          "stack-db",
		Name:        "database",
		ComposeYAML: composeContent,
		DefaultEnv:  "POSTGRES_PASSWORD=secret",
	})
	_ = srv.Store().CreateDeployment(store.Deployment{
		ID:      "dep-db",
		StackID: "stack-db",
		NodeID:  "node-git",
		EnvVars: "POSTGRES_PASSWORD=custom-secret\nPORT=5432",
		Status:  "running",
	})

	// 4. Trigger "Sync Now" via POST /settings/git/sync
	req = httptest.NewRequest(http.MethodPost, "/settings/git/sync", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after sync now, got %d", rec.Code)
	}

	// 5. Verify files provided to GitSyncer
	if mockGit.syncCount == 0 {
		t.Fatalf("expected GitSyncer.Sync to be called")
	}

	expectedComposePath := "stacks/database/docker-compose.yml"
	expectedEnvPath := "stacks/database/.env.example"
	expectedDepEnvPath := "deployments/Prod-Node-1/database.env"
	expectedInventory := "inventory.json"

	if string(mockGit.syncedFiles[expectedComposePath]) != composeContent {
		t.Errorf("expected compose file in git mirror, got %s", string(mockGit.syncedFiles[expectedComposePath]))
	}
	if !strings.Contains(string(mockGit.syncedFiles[expectedEnvPath]), "POSTGRES_PASSWORD=secret") {
		t.Errorf("expected default env file in git mirror, got %s", string(mockGit.syncedFiles[expectedEnvPath]))
	}
	if !strings.Contains(string(mockGit.syncedFiles[expectedDepEnvPath]), "POSTGRES_PASSWORD=custom-secret") {
		t.Errorf("expected deployment env file in git mirror, got %s", string(mockGit.syncedFiles[expectedDepEnvPath]))
	}
	if !strings.Contains(string(mockGit.syncedFiles[expectedInventory]), "Prod-Node-1") {
		t.Errorf("expected inventory.json to contain node name, got %s", string(mockGit.syncedFiles[expectedInventory]))
	}

	// 6. Verify Git status saved in store
	gitStatus, _ := srv.Store().GetSetting("git_mirror_status")
	if gitStatus != "synced" {
		t.Errorf("expected git_mirror_status 'synced', got %q", gitStatus)
	}
}

func TestServer_NotesAndRunbookSync(t *testing.T) {
	mockExec := &mockDeployExecutor{}
	mockGit := &mockGitSyncer{}

	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
		Executor:       mockExec,
		GitSyncer:      mockGit,
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

	// 2. Create Node with notes
	nodeForm := url.Values{
		"name":  {"Node-Alpha"},
		"host":  {"10.0.0.1"},
		"notes": {"Hetzner Falkenstein (4 vCPU, 8GB RAM). PIC: Budi."},
	}
	req = httptest.NewRequest(http.MethodPost, "/nodes", strings.NewReader(nodeForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	nodes, _ := srv.Store().ListNodes()
	if len(nodes) != 1 || nodes[0].Notes != "Hetzner Falkenstein (4 vCPU, 8GB RAM). PIC: Budi." {
		t.Fatalf("expected node notes to persist, got: %+v", nodes)
	}

	// 3. Create Stack with notes
	stackForm := url.Values{
		"name":         {"api-gateway"},
		"compose_yaml": {"services:\n  gateway:\n    image: traefik:v3\n"},
		"notes":        {"Expose port 80/443. Requires external network proxy-net."},
	}
	req = httptest.NewRequest(http.MethodPost, "/stacks", strings.NewReader(stackForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	stacks, _ := srv.Store().ListStacks()
	if len(stacks) != 1 || stacks[0].Notes != "Expose port 80/443. Requires external network proxy-net." {
		t.Fatalf("expected stack notes to persist, got: %+v", stacks)
	}

	// 4. Create Deployment with notes
	depForm := url.Values{
		"stack_id": {stacks[0].ID},
		"node_id":  {nodes[0].ID},
		"notes":    {"Production instance for PT Maju Mundur"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments", strings.NewReader(depForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	deps, _ := srv.Store().ListDeployments()
	if len(deps) != 1 || deps[0].Notes != "Production instance for PT Maju Mundur" {
		t.Fatalf("expected deployment notes to persist, got: %+v", deps)
	}

	// 5. Update Runbook via POST /settings/runbook
	runbookForm := url.Values{
		"runbook_notes": {"# Operator Runbook\n- Maintenance window: Friday 23:00\n- Primary DNS: 1.1.1.1\n"},
	}
	req = httptest.NewRequest(http.MethodPost, "/settings/runbook", strings.NewReader(runbookForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after runbook update, got %d", rec.Code)
	}

	savedNotes, _ := srv.Store().GetSetting("global_runbook_notes")
	if !strings.Contains(savedNotes, "Maintenance window: Friday 23:00") {
		t.Fatalf("expected runbook notes saved in store, got %q", savedNotes)
	}

	// 6. Trigger Sync and check RUNBOOK.md and notes in inventory.json
	req = httptest.NewRequest(http.MethodPost, "/settings/git/sync", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	runbookFile := string(mockGit.syncedFiles["RUNBOOK.md"])
	if !strings.Contains(runbookFile, "Maintenance window: Friday 23:00") {
		t.Errorf("expected RUNBOOK.md in git mirror sync, got: %s", runbookFile)
	}

	inventoryFile := string(mockGit.syncedFiles["inventory.json"])
	if !strings.Contains(inventoryFile, "Hetzner Falkenstein") || !strings.Contains(inventoryFile, "PT Maju Mundur") {
		t.Errorf("expected inventory.json to contain notes, got: %s", inventoryFile)
	}
}
