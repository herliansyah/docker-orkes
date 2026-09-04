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



