package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"docker-orkes/internal/logger"
	"docker-orkes/internal/server"
)

func TestServer_TelemetryStreamAuthAndHydration(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	srv, err := server.New(server.Config{
		DBPath:         dbPath,
		MasterPassword: "secret-password",
		Executor:       &mockObsExecutor{},
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Unauthenticated request to /api/telemetry/stream should return 401
	unauthReq, _ := http.NewRequest("GET", ts.URL+"/api/telemetry/stream", nil)
	unauthResp, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatalf("unauth request failed: %v", err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", unauthResp.StatusCode)
	}

	// 2. Pre-populate broadcaster ring buffer
	broadcaster := logger.DefaultBroadcaster()
	broadcaster.Broadcast(logger.Event{
		Timestamp: time.Now(),
		Level:     "INFO",
		Module:    "deploy",
		Target:    "node-alpha",
		Message:   "stack deployed successfully",
		Attrs: map[string]any{
			"stack": "web-stack",
		},
	})

	// 3. Authenticate with session cookie
	sessionToken := "test-session-token-12345"
	if err := srv.Store().CreateSession(sessionToken); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/telemetry/stream", nil)
	req.AddCookie(&http.Cookie{
		Name:  "orkes_session",
		Value: sessionToken,
	})

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("streaming request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
	}

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %s", ct)
	}

	// 4. Read first event from stream
	reader := bufio.NewReader(resp.Body)
	var eventData string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read line from stream: %v", err)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			eventData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}

	var evt logger.Event
	if err := json.Unmarshal([]byte(eventData), &evt); err != nil {
		t.Fatalf("failed to unmarshal SSE event json: %v, raw: %s", err, eventData)
	}

	if evt.Level != "INFO" {
		t.Errorf("expected Level INFO, got %s", evt.Level)
	}
	if evt.Module != "deploy" {
		t.Errorf("expected Module deploy, got %s", evt.Module)
	}
	if evt.Target != "node-alpha" {
		t.Errorf("expected Target node-alpha, got %s", evt.Target)
	}
	if evt.Message != "stack deployed successfully" {
		t.Errorf("expected Message 'stack deployed successfully', got %s", evt.Message)
	}
}
