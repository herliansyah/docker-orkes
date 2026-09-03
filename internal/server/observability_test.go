package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"docker-orkes/internal/server"
	"docker-orkes/internal/store"
)

type mockObsExecutor struct {
	mockDeployExecutor
	dfOutput       string
	dockerPsOutput string
	dockerLogOut   string
}

func (m *mockObsExecutor) RunCommand(ctx context.Context, host string, port int, user string, privKey string, cmd string) (string, error) {
	if strings.HasPrefix(cmd, "df") {
		return m.dfOutput, nil
	}
	if strings.Contains(cmd, "docker ps") {
		return m.dockerPsOutput, nil
	}
	if strings.Contains(cmd, "docker logs") {
		return m.dockerLogOut, nil
	}
	return m.mockDeployExecutor.RunCommand(ctx, host, port, user, privKey, cmd)
}

func TestServer_NodeObservabilityAndLogs(t *testing.T) {
	mockExec := &mockObsExecutor{
		dfOutput: "Filesystem      Size  Used Avail Use% Mounted on\n/dev/root        50G   15G   35G  30% /\n",
		dockerPsOutput: `{"ID":"c123","Names":"web-app-1","Image":"nginx:alpine","Status":"Up 2 hours","State":"running","Ports":"0.0.0.0:80->80/tcp"}
{"ID":"c456","Names":"redis-db","Image":"redis:7","Status":"Exited (0) 5 mins ago","State":"exited","Ports":""}
`,
		dockerLogOut: "2026-09-03 10:00:00 [notice] 1#1: start worker processes\n2026-09-03 10:00:01 HTTP 200 GET /\n",
	}

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

	// 2. Register Node
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-obs",
		Name:   "Telemetry Node",
		Host:   "10.0.0.88",
		Port:   22,
		User:   "root",
		Status: "online",
	})

	// 3. Query GET /nodes/node-obs/telemetry
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-obs/telemetry", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for telemetry, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var telemetry struct {
		Disk struct {
			Total   string `json:"total"`
			Used    string `json:"used"`
			Free    string `json:"free"`
			Percent string `json:"percent"`
		} `json:"disk"`
		Containers []struct {
			ID    string `json:"id"`
			Names string `json:"names"`
			Image string `json:"image"`
			State string `json:"state"`
		} `json:"containers"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &telemetry); err != nil {
		t.Fatalf("failed to unmarshal telemetry json: %v, body: %s", err, rec.Body.String())
	}

	if telemetry.Disk.Total != "50G" || telemetry.Disk.Used != "15G" || telemetry.Disk.Percent != "30%" {
		t.Errorf("unexpected disk metrics: %+v", telemetry.Disk)
	}

	if len(telemetry.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(telemetry.Containers))
	}
	if telemetry.Containers[0].Names != "web-app-1" || telemetry.Containers[0].State != "running" {
		t.Errorf("unexpected container 0: %+v", telemetry.Containers[0])
	}
	if telemetry.Containers[1].Names != "redis-db" || telemetry.Containers[1].State != "exited" {
		t.Errorf("unexpected container 1: %+v", telemetry.Containers[1])
	}

	// 4. Query GET /nodes/node-obs/containers/c123/logs
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-obs/containers/c123/logs", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for logs, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "start worker processes") {
		t.Errorf("expected container logs to contain 'start worker processes', got: %s", rec.Body.String())
	}

	// 5. Query GET /nodes/node-obs HTML page
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-obs", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for node detail view, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Telemetry Node") {
		t.Errorf("expected node detail view to contain node name")
	}
}
