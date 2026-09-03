package server_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"docker-orkes/internal/server"
)


type mockExecutor struct {
	testConnFunc func(ctx context.Context, host string, port int, user string, privKey string) (string, error)
}

func (m *mockExecutor) TestConnection(ctx context.Context, host string, port int, user string, privKey string) (string, error) {
	if m.testConnFunc != nil {
		return m.testConnFunc(ctx, host, port, user, privKey)
	}
	return "Docker version 27.1.1, build 6312585", nil
}

func (m *mockExecutor) RunCommand(ctx context.Context, host string, port int, user string, privKey string, cmd string) (string, error) {
	return "", nil
}

func (m *mockExecutor) RunCommandStream(ctx context.Context, host string, port int, user string, privKey string, cmd string, out io.Writer) error {
	return nil
}

func (m *mockExecutor) UploadFiles(ctx context.Context, host string, port int, user string, privKey string, remoteDir string, files map[string][]byte) error {
	return nil
}

func (m *mockExecutor) ReadRemoteFile(ctx context.Context, host string, port int, user string, privKey string, remoteFilePath string) ([]byte, error) {
	return nil, nil
}

func (m *mockExecutor) BootstrapKey(ctx context.Context, host string, port int, user string, password string, pubKey string) error {
	return nil
}



func TestServer_NodeInventoryAndTestConnection(t *testing.T) {
	mockExec := &mockExecutor{}

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

	// 1. Authenticate to get session cookie
	loginForm := url.Values{"password": {"secret-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// 2. Add a new Node via POST /nodes
	nodeForm := url.Values{
		"name": {"Production Server 1"},
		"host": {"192.168.1.50"},
		"port": {"22"},
		"user": {"ubuntu"},
	}
	req = httptest.NewRequest(http.MethodPost, "/nodes", strings.NewReader(nodeForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after node creation, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 3. View dashboard / nodes list to see newly added node
	req = httptest.NewRequest(http.MethodGet, "/nodes", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /nodes, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Production Server 1") || !strings.Contains(body, "192.168.1.50") {
		t.Errorf("expected node list to contain 'Production Server 1' and '192.168.1.50'")
	}

	// 4. Test connection - successful case
	mockExec.testConnFunc = func(ctx context.Context, host string, port int, user string, privKey string) (string, error) {
		if host != "192.168.1.50" || user != "ubuntu" {
			return "", errors.New("invalid credentials")
		}
		return "Docker version 27.1.1", nil
	}

	// Trigger POST /nodes/{id}/test
	// We extract node ID from the list page or query store
	nodes, err := srv.Store().ListNodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("expected 1 node in store, got %d (err: %v)", len(nodes), err)
	}
	nodeID := nodes[0].ID

	req = httptest.NewRequest(http.MethodPost, "/nodes/"+nodeID+"/test", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 200 or redirect after test connection, got %d", rec.Code)
	}

	// Check updated node status
	node, err := srv.Store().GetNode(nodeID)
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}
	if node.Status != "online" {
		t.Errorf("expected node status 'online', got %q", node.Status)
	}
	if !strings.Contains(node.DockerVersion, "Docker version 27.1.1") {
		t.Errorf("expected docker version saved, got %q", node.DockerVersion)
	}

	// 5. Test connection - failure case
	mockExec.testConnFunc = func(ctx context.Context, host string, port int, user string, privKey string) (string, error) {
		return "", errors.New("permission denied (publickey)")
	}

	req = httptest.NewRequest(http.MethodPost, "/nodes/"+nodeID+"/test", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	node, err = srv.Store().GetNode(nodeID)
	if err != nil {
		t.Fatalf("failed to get node: %v", err)
	}
	if node.Status != "offline" {
		t.Errorf("expected node status 'offline', got %q", node.Status)
	}
	if !strings.Contains(node.LastError, "permission denied") {
		t.Errorf("expected last error to record failure, got %q", node.LastError)
	}

	// 6. Delete Node via POST /nodes/{id}/delete
	req = httptest.NewRequest(http.MethodPost, "/nodes/"+nodeID+"/delete", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after node deletion, got %d", rec.Code)
	}

	nodes, err = srv.Store().ListNodes()
	if err != nil || len(nodes) != 0 {
		t.Errorf("expected 0 nodes after deletion, got %d", len(nodes))
	}
}
