package server_test

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"docker-orkes/internal/server"
	"docker-orkes/internal/ssh"
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

func (m *mockDeployExecutor) BootstrapKey(ctx context.Context, host string, port int, user string, password string, pubKey string) error {
	return nil
}

func (m *mockDeployExecutor) ListRemoteFiles(ctx context.Context, host string, port int, user string, privKey string, remoteDir string) ([]ssh.RemoteFileEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []ssh.RemoteFileEntry
	for k, v := range m.uploaded {
		if strings.HasPrefix(k, remoteDir+"/") {
			rel := strings.TrimPrefix(k, remoteDir+"/")
			entries = append(entries, ssh.RemoteFileEntry{
				Name:    rel,
				Size:    int64(len(v)),
				IsDir:   false,
				ModTime: "Sep 04 08:00",
				Mode:    "-rw-r--r--",
			})
		}
	}
	return entries, nil
}

func (m *mockDeployExecutor) RunInteractiveTerminal(ctx context.Context, host string, port int, user string, privKey string, cmd string, rows int, cols int, rw io.ReadWriter, resizeChan <-chan [2]int) error {
	return nil
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

	// 10. Update deployment env via POST /deployments/{id}/update
	updateEnvForm := url.Values{
		"env_vars": {"PORT=9090\nNEW_VAR=hello"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/update", strings.NewReader(updateEnvForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after env update, got %d", rec.Code)
	}

	depAfterUpdate, err := srv.Store().GetDeployment(deployID)
	if err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}
	if !strings.Contains(depAfterUpdate.EnvVars, "PORT=9090") {
		t.Errorf("expected updated env_vars to contain PORT=9090, got %q", depAfterUpdate.EnvVars)
	}

	// 11. Browse remote files via GET /deployments/{id}/files
	req = httptest.NewRequest(http.MethodGet, "/deployments/"+deployID+"/files", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for file list, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "docker-compose.yml") {
		t.Errorf("expected file list to contain docker-compose.yml, got %s", rec.Body.String())
	}

	// 12. View file content via GET /deployments/{id}/files/view
	req = httptest.NewRequest(http.MethodGet, "/deployments/"+deployID+"/files/view?file=docker-compose.yml", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for file view, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "nginx:alpine") {
		t.Errorf("expected file content to contain nginx:alpine, got %s", rec.Body.String())
	}

	// 13. Direct edit & save file via POST /deployments/{id}/files/save with redeploy=true
	saveForm := url.Values{
		"file":     {"docker-compose.yml"},
		"content":  {"services:\n  web:\n    image: nginx:latest\n"},
		"redeploy": {"true"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/files/save", strings.NewReader(saveForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for file save, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Errorf("expected success:true in save response, got %s", rec.Body.String())
	}

	// Verify file was updated in mockExec
	if string(mockExec.uploaded[".docker-orkes/stacks/web-proxy/docker-compose.yml"]) != "services:\n  web:\n    image: nginx:latest\n" {
		t.Errorf("expected updated compose file in mock executor, got: %s", string(mockExec.uploaded[".docker-orkes/stacks/web-proxy/docker-compose.yml"]))
	}

	// 14. Create folder via POST /deployments/{id}/files/mkdir
	mkdirForm := url.Values{
		"path": {"conf.d/vhosts"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/files/mkdir", strings.NewReader(mkdirForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected 200 OK for mkdir, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 15. Upload files/folders via POST /deployments/{id}/files/upload
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("subpath", "conf.d")
	fw, _ := w.CreateFormFile("files", "site.conf")
	_, _ = fw.Write([]byte("server { listen 80; }"))
	w.Close()

	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/files/upload", &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"files_uploaded":1`) {
		t.Fatalf("expected 200 OK for upload, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if string(mockExec.uploaded[".docker-orkes/stacks/web-proxy/conf.d/site.conf"]) != "server { listen 80; }" {
		t.Errorf("uploaded file not found in mockExec: %+v", mockExec.uploaded)
	}

	// 15b. Upload folder structure (e.g. ABCD/file.txt and ABCD/sub/file2.txt) via paths field
	var bFolder bytes.Buffer
	wFolder := multipart.NewWriter(&bFolder)
	_ = wFolder.WriteField("paths", "ABCD/file.txt")
	fw1, _ := wFolder.CreateFormFile("files", "file.txt")
	_, _ = fw1.Write([]byte("abcd file"))
	_ = wFolder.WriteField("paths", "ABCD/sub/file2.txt")
	fw2, _ := wFolder.CreateFormFile("files", "file2.txt")
	_, _ = fw2.Write([]byte("sub file"))
	wFolder.Close()

	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/files/upload", &bFolder)
	req.Header.Set("Content-Type", wFolder.FormDataContentType())
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"files_uploaded":2`) {
		t.Fatalf("expected 200 OK for folder upload, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	if string(mockExec.uploaded[".docker-orkes/stacks/web-proxy/ABCD/file.txt"]) != "abcd file" {
		t.Errorf("expected ABCD/file.txt in uploaded files, got: %+v", mockExec.uploaded)
	}
	if string(mockExec.uploaded[".docker-orkes/stacks/web-proxy/ABCD/sub/file2.txt"]) != "sub file" {
		t.Errorf("expected ABCD/sub/file2.txt in uploaded files, got: %+v", mockExec.uploaded)
	}

	// 16. Extract archive via POST /deployments/{id}/files/extract
	extractForm := url.Values{
		"file":       {"bundle.tar.gz"},
		"target_dir": {"extracted"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/files/extract", strings.NewReader(extractForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected 200 OK for extract, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 17. Delete file via POST /deployments/{id}/files/delete
	// 17a. Protected check: cannot delete docker-compose.yml
	delForm := url.Values{"path": {"docker-compose.yml"}}
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/files/delete", strings.NewReader(delForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "protected") {
		t.Fatalf("expected 400 Bad Request for protected docker-compose.yml delete, got %d", rec.Code)
	}

	// 17b. Delete auxiliary file
	delForm = url.Values{"path": {"conf.d/site.conf"}}
	req = httptest.NewRequest(http.MethodPost, "/deployments/"+deployID+"/files/delete", strings.NewReader(delForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected 200 OK for auxiliary file delete, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}
func TestServer_DeploymentDeletionLifecycle(t *testing.T) {
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

	if err := srv.Store().CreateStack(store.Stack{
		ID:          "stack-1",
		Name:        "web-app",
		ComposeYAML: "services:\n  web:\n    image: nginx:alpine\n",
	}); err != nil {
		t.Fatalf("failed to create stack: %v", err)
	}

	// 1. Test Detach (action=detach)
	// Detach must delete metadata from store, but NOT run docker compose down on node.
	dep1 := store.Deployment{
		ID:      "dep-detach",
		StackID: "stack-1",
		NodeID:  "node-1",
		Status:  "running",
	}
	if err := srv.Store().CreateDeployment(dep1); err != nil {
		t.Fatalf("failed to create deployment: %v", err)
	}

	mockExec.mu.Lock()
	mockExec.executedCmds = nil
	mockExec.mu.Unlock()

	detachForm := url.Values{
		"action": {"detach"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments/dep-detach/delete", strings.NewReader(detachForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after detach delete, got %d", rec.Code)
	}

	if _, err := srv.Store().GetDeployment("dep-detach"); err == nil {
		t.Errorf("expected dep-detach to be removed from store")
	}

	mockExec.mu.Lock()
	for _, cmd := range mockExec.executedCmds {
		if strings.Contains(cmd, "docker compose") && strings.Contains(cmd, "down") {
			t.Errorf("detach should NOT run docker compose down, found command: %s", cmd)
		}
	}
	mockExec.mu.Unlock()

	// 2. Test Purge Safety Validation
	// Purge volumes requires confirm_name == stack name
	dep2 := store.Deployment{
		ID:      "dep-purge-check",
		StackID: "stack-1",
		NodeID:  "node-1",
		Status:  "running",
	}
	if err := srv.Store().CreateDeployment(dep2); err != nil {
		t.Fatalf("failed to create deployment: %v", err)
	}

	purgeBadForm := url.Values{
		"action":        {"teardown"},
		"purge_volumes": {"true"},
		"confirm_name":  {"wrong-name"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments/dep-purge-check/delete", strings.NewReader(purgeBadForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when confirm_name does not match, got %d", rec.Code)
	}
	if _, err := srv.Store().GetDeployment("dep-purge-check"); err != nil {
		t.Errorf("deployment should not be deleted on failed confirmation")
	}

	// 3. Test Teardown with Purge Volumes & Confirmation
	purgeGoodForm := url.Values{
		"action":        {"teardown"},
		"purge_volumes": {"true"},
		"confirm_name":  {"web-app"},
	}
	mockExec.mu.Lock()
	mockExec.executedCmds = nil
	mockExec.mu.Unlock()

	req = httptest.NewRequest(http.MethodPost, "/deployments/dep-purge-check/delete", strings.NewReader(purgeGoodForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after successful purge delete, got %d", rec.Code)
	}

	mockExec.mu.Lock()
	var hasDownV bool
	var hasRmStackDir bool
	for _, cmd := range mockExec.executedCmds {
		if strings.Contains(cmd, "docker compose") && strings.Contains(cmd, "down -v") {
			hasDownV = true
		}
		if strings.Contains(cmd, "rm -rf") && strings.Contains(cmd, ".docker-orkes/stacks/web-app") {
			hasRmStackDir = true
		}
	}
	mockExec.mu.Unlock()

	if !hasDownV {
		t.Errorf("expected docker compose down -v command, executed: %v", mockExec.executedCmds)
	}
	if !hasRmStackDir {
		t.Errorf("expected rm -rf stack directory command, executed: %v", mockExec.executedCmds)
	}

	// 4. Test Standard Teardown (no purge)
	dep3 := store.Deployment{
		ID:      "dep-teardown",
		StackID: "stack-1",
		NodeID:  "node-1",
		Status:  "running",
	}
	if err := srv.Store().CreateDeployment(dep3); err != nil {
		t.Fatalf("failed to create deployment: %v", err)
	}

	mockExec.mu.Lock()
	mockExec.executedCmds = nil
	mockExec.mu.Unlock()

	teardownForm := url.Values{
		"action": {"teardown"},
	}
	req = httptest.NewRequest(http.MethodPost, "/deployments/dep-teardown/delete", strings.NewReader(teardownForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after teardown delete, got %d", rec.Code)
	}

	mockExec.mu.Lock()
	var hasDownNormal bool
	for _, cmd := range mockExec.executedCmds {
		if strings.Contains(cmd, "docker compose") && strings.Contains(cmd, "down") && !strings.Contains(cmd, "-v") {
			hasDownNormal = true
		}
	}
	mockExec.mu.Unlock()

	if !hasDownNormal {
		t.Errorf("expected standard docker compose down command, executed: %v", mockExec.executedCmds)
	}
}
func TestServer_DeploymentPreflightAndUp(t *testing.T) {
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
	reqLogin := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	reqLogin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recLogin := httptest.NewRecorder()
	handler.ServeHTTP(recLogin, reqLogin)
	cookie := recLogin.Result().Cookies()[0]

	_ = srv.Store().CreateNode(store.Node{ID: "n1", Name: "node-1", Host: "10.0.0.1", Port: 22, User: "root", Status: "online"})
	_ = srv.Store().CreateStack(store.Stack{ID: "s1", Name: "app", ComposeYAML: "services:\n  web:\n    image: nginx\n"})
	_ = srv.Store().CreateDeployment(store.Deployment{
		ID:      "d1",
		StackID: "s1",
		NodeID:  "n1",
		EnvVars: "PORT=8080\nSECRET_KEY=supersecret123",
		Status:  "running",
	})

	// 1. Preflight check endpoint (GET /deployments/{id}/preflight)
	req := httptest.NewRequest(http.MethodGet, "/deployments/d1/preflight", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from preflight, got %d", rec.Code)
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, `"success":true`) || !strings.Contains(bodyStr, `"d1"`) {
		t.Errorf("unexpected preflight body: %s", bodyStr)
	}

	// 2. Up deployment with JSON accept header (POST /deployments/{id}/up)
	reqUp := httptest.NewRequest(http.MethodPost, "/deployments/d1/up", nil)
	reqUp.Header.Set("Accept", "application/json")
	reqUp.AddCookie(cookie)
	recUp := httptest.NewRecorder()
	handler.ServeHTTP(recUp, reqUp)

	if recUp.Code != http.StatusOK {
		t.Fatalf("expected 200 from JSON Up action, got %d: %s", recUp.Code, recUp.Body.String())
	}
	upBody := recUp.Body.String()
	if !strings.Contains(upBody, `"success":true`) {
		t.Errorf("expected up success true, got: %s", upBody)
	}

	// 3. Rollout deployment with JSON accept header
	reqRollout := httptest.NewRequest(http.MethodPost, "/deployments/d1/rollout", nil)
	reqRollout.Header.Set("Accept", "application/json")
	reqRollout.AddCookie(cookie)
	recRollout := httptest.NewRecorder()
	handler.ServeHTTP(recRollout, reqRollout)
	if recRollout.Code != http.StatusOK || !strings.Contains(recRollout.Body.String(), `"success":true`) {
		t.Errorf("expected rollout success, got: %s", recRollout.Body.String())
	}

	// 4. Restart deployment with JSON accept header
	reqRestart := httptest.NewRequest(http.MethodPost, "/deployments/d1/restart", nil)
	reqRestart.Header.Set("Accept", "application/json")
	reqRestart.AddCookie(cookie)
	recRestart := httptest.NewRecorder()
	handler.ServeHTTP(recRestart, reqRestart)
	if recRestart.Code != http.StatusOK || !strings.Contains(recRestart.Body.String(), `"success":true`) {
		t.Errorf("expected restart success, got: %s", recRestart.Body.String())
	}

	// 5. Down deployment with JSON accept header
	reqDown := httptest.NewRequest(http.MethodPost, "/deployments/d1/down", nil)
	reqDown.Header.Set("Accept", "application/json")
	reqDown.AddCookie(cookie)
	recDown := httptest.NewRecorder()
	handler.ServeHTTP(recDown, reqDown)
	if recDown.Code != http.StatusOK || !strings.Contains(recDown.Body.String(), `"success":true`) {
		t.Errorf("expected down success, got: %s", recDown.Body.String())
	}
}

func TestServer_OfflineNodeDeploymentSafety(t *testing.T) {
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
	reqLogin := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	reqLogin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recLogin := httptest.NewRecorder()
	handler.ServeHTTP(recLogin, reqLogin)
	cookie := recLogin.Result().Cookies()[0]

	// Seed offline node and stack
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-offline",
		Name:   "Offline Node",
		Host:   "10.0.0.99",
		Port:   22,
		Status: "offline",
	})
	_ = srv.Store().CreateStack(store.Stack{
		ID:          "stack-off",
		Name:        "off-stack",
		ComposeYAML: "services:\n  web:\n    image: nginx\n",
	})
	_ = srv.Store().CreateDeployment(store.Deployment{
		ID:          "dep-offline",
		StackID:     "stack-off",
		NodeID:      "node-offline",
		Status:      "running",
		DriftStatus: "synced",
	})

	// 1. Verify store query loads NodeStatus
	deps, err := srv.Store().ListDeployments()
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(deps) != 1 || deps[0].NodeStatus != "offline" {
		t.Fatalf("expected NodeStatus 'offline', got %+v", deps)
	}

	// 2. Query containers on offline node
	reqContainers := httptest.NewRequest(http.MethodGet, "/deployments/dep-offline/containers", nil)
	reqContainers.AddCookie(cookie)
	recContainers := httptest.NewRecorder()
	handler.ServeHTTP(recContainers, reqContainers)
	if !strings.Contains(recContainers.Body.String(), "is offline") {
		t.Errorf("expected container check to report offline node, got: %s", recContainers.Body.String())
	}

	// 3. Drift check on offline node
	reqDrift := httptest.NewRequest(http.MethodPost, "/deployments/dep-offline/drift/check", nil)
	reqDrift.Header.Set("Accept", "application/json")
	reqDrift.AddCookie(cookie)
	recDrift := httptest.NewRecorder()
	handler.ServeHTTP(recDrift, reqDrift)
	if recDrift.Code != http.StatusInternalServerError || !strings.Contains(recDrift.Body.String(), "offline") {
		t.Errorf("expected drift check error for offline node, got %d: %s", recDrift.Code, recDrift.Body.String())
	}

	// 4. Dashboard renders Node Offline badge and disabled actions
	reqDash := httptest.NewRequest(http.MethodGet, "/", nil)
	reqDash.AddCookie(cookie)
	recDash := httptest.NewRecorder()
	handler.ServeHTTP(recDash, reqDash)
	if recDash.Code != http.StatusOK {
		t.Fatalf("expected 200 from dashboard, got %d", recDash.Code)
	}
	dashHTML := recDash.Body.String()
	if !strings.Contains(dashHTML, "Node Offline") {
		t.Errorf("expected dashboard to display 'Node Offline' badge")
	}
	if !strings.Contains(dashHTML, `data-status="offline"`) {
		t.Errorf("expected deployment row data-status='offline'")
	}
}

func TestServer_DuplicateDeploymentAndFlagInjectionGuards(t *testing.T) {
	mockExec := &mockDeployExecutor{}
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
		Executor:       mockExec,
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// Login
	loginForm := url.Values{"password": {"secret-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// Create Node & Stack
	_ = srv.Store().CreateNode(store.Node{ID: "n-guard", Name: "Guard Node", Host: "10.0.0.1", Port: 22, User: "root", Status: "online"})
	_ = srv.Store().CreateStack(store.Stack{ID: "s-guard", Name: "guard-stack", ComposeYAML: "services:\n  web:\n    image: nginx\n"})

	// 1. First deployment should succeed
	deployForm := url.Values{
		"stack_id": {"s-guard"},
		"node_id":  {"n-guard"},
		"env_vars": {"A=1"},
	}
	req1 := httptest.NewRequest(http.MethodPost, "/deployments", strings.NewReader(deployForm.Encode()))
	req1.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req1.AddCookie(cookie)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusSeeOther && rec1.Code != http.StatusFound {
		t.Fatalf("expected redirect for first deployment, got %d", rec1.Code)
	}

	// 2. Second deployment on same node with same stack must be rejected with 400
	req2 := httptest.NewRequest(http.MethodPost, "/deployments", strings.NewReader(deployForm.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for duplicate deployment, got %d", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "already exists") {
		t.Errorf("expected duplicate error message, got: %s", rec2.Body.String())
	}

	// 3. Test leading hyphen rejection on Docker commands
	// 3a. Pull image starting with hyphen
	reqPull := httptest.NewRequest(http.MethodPost, "/nodes/n-guard/images/pull", strings.NewReader(`{"image_ref":"--help"}`))
	reqPull.Header.Set("Content-Type", "application/json")
	reqPull.AddCookie(cookie)
	recPull := httptest.NewRecorder()
	handler.ServeHTTP(recPull, reqPull)
	if recPull.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for image_ref with leading hyphen, got %d", recPull.Code)
	}

	// 3b. Delete image starting with hyphen
	reqDelImg := httptest.NewRequest(http.MethodPost, "/nodes/n-guard/images/delete", strings.NewReader(`{"image_ref":"-f"}`))
	reqDelImg.Header.Set("Content-Type", "application/json")
	reqDelImg.AddCookie(cookie)
	recDelImg := httptest.NewRecorder()
	handler.ServeHTTP(recDelImg, reqDelImg)
	if recDelImg.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for image delete with leading hyphen, got %d", recDelImg.Code)
	}

	// 3c. Delete volume starting with hyphen
	reqDelVol := httptest.NewRequest(http.MethodPost, "/nodes/n-guard/volumes/delete", strings.NewReader(`{"name":"--all"}`))
	reqDelVol.Header.Set("Content-Type", "application/json")
	reqDelVol.AddCookie(cookie)
	recDelVol := httptest.NewRecorder()
	handler.ServeHTTP(recDelVol, reqDelVol)
	if recDelVol.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for volume delete with leading hyphen, got %d", recDelVol.Code)
	}

	// 3d. Delete network starting with hyphen
	reqDelNet := httptest.NewRequest(http.MethodPost, "/nodes/n-guard/networks/delete", strings.NewReader(`{"name":"-f"}`))
	reqDelNet.Header.Set("Content-Type", "application/json")
	reqDelNet.AddCookie(cookie)
	recDelNet := httptest.NewRecorder()
	handler.ServeHTTP(recDelNet, reqDelNet)
	if recDelNet.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for network delete with leading hyphen, got %d", recDelNet.Code)
	}
}
