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

func TestServer_BootstrapAndAuth(t *testing.T) {
	// In-memory test SQLite
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// 1. Unauthenticated request to "/" redirects to "/login"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Errorf("expected redirect status (302/303), got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect to /login, got %q", loc)
	}

	// 2. GET "/login" returns 200 with login form
	req = httptest.NewRequest(http.MethodGet, "/login", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /login, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "password") {
		t.Errorf("expected login page to contain password field")
	}

	// 3. POST "/login" with wrong password fails
	formData := url.Values{"password": {"wrong-password"}}
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for wrong password, got %d", rec.Code)
	}

	// 4. POST "/login" with correct password succeeds and sets session cookie
	formData = url.Values{"password": {"secret-password"}}
	req = httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Errorf("expected redirect on successful login, got %d", rec.Code)
	}
	cookies := rec.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "orkes_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected session cookie 'orkes_session' to be set")
	}

	// 5. Authenticated GET "/" renders dashboard containing SSH public key
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for authenticated dashboard, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "ssh-ed25519 ") {
		t.Errorf("expected dashboard body to contain generated ssh-ed25519 public key, got: %s", body)
	}
}

func TestServer_KeypairIdempotency(t *testing.T) {
	tempDB := t.TempDir() + "/orkes_test.db"

	// Boot 1
	srv1, err := server.New(server.Config{
		DBPath:         tempDB,
		MasterPassword: "pass",
	})
	if err != nil {
		t.Fatalf("boot 1 failed: %v", err)
	}
	key1 := srv1.PublicKeySSH()
	srv1.Close()

	// Boot 2
	srv2, err := server.New(server.Config{
		DBPath:         tempDB,
		MasterPassword: "pass",
	})
	if err != nil {
		t.Fatalf("boot 2 failed: %v", err)
	}
	defer srv2.Close()
	key2 := srv2.PublicKeySSH()

	if key1 != key2 {
		t.Errorf("expected key to be idempotent across boots, got %q vs %q", key1, key2)
	}
}

func TestServer_RevisionsRollbackAndSettings(t *testing.T) {
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "admin",
		Executor:       &mockDeployExecutor{},
	})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	defer srv.Close()

	st := srv.Store()
	sessionToken := "test-session"
	_ = st.CreateSession(sessionToken)
	sessionCookie := &http.Cookie{Name: "orkes_session", Value: sessionToken}

	handler := srv.Handler()

	// 1. Setup Node, Stack, Deployment
	_ = st.CreateNode(store.Node{ID: "n1", Name: "node-1", Host: "10.0.0.1", Port: 22, User: "root"})
	_ = st.CreateStack(store.Stack{ID: "s1", Name: "app", ComposeYAML: "image: app:v1\n"})
	_ = st.CreateDeployment(store.Deployment{ID: "d1", StackID: "s1", NodeID: "n1", EnvVars: "V=1"})

	// Deploy initially via controlplane
	if err := srv.ControlPlane().Deploy(context.Background(), "d1"); err != nil {
		t.Fatalf("deploy initial: %v", err)
	}

	// 2. GET /deployments/{id}/revisions
	req := httptest.NewRequest(http.MethodGet, "/deployments/d1/revisions", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for revisions, got %d", rec.Code)
	}
	var revs []store.DeploymentRevision
	if err := json.Unmarshal(rec.Body.Bytes(), &revs); err != nil || len(revs) != 1 {
		t.Fatalf("expected 1 revision in response, got %d (err: %v)", len(revs), err)
	}
	rev1ID := revs[0].ID

	// Deploy v2
	_ = st.UpdateDeploymentEnv("d1", "V=2", "")
	_ = srv.ControlPlane().Deploy(context.Background(), "d1")

	// 3. POST /deployments/{id}/rollback to rev1
	formData := url.Values{"revision_id": {rev1ID}}
	req = httptest.NewRequest(http.MethodPost, "/deployments/d1/rollback", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on rollback, got %d", rec.Code)
	}

	revsAfter, _ := srv.ControlPlane().ListRevisions(context.Background(), "d1")
	if len(revsAfter) != 3 {
		t.Fatalf("expected 3 revisions after forward-only rollback, got %d", len(revsAfter))
	}
	if revsAfter[0].RevisionNumber != 3 || revsAfter[0].EnvVars != "V=1" {
		t.Errorf("expected rev 3 to have V=1, got %+v", revsAfter[0])
	}

	// 4. POST /settings/notifications
	notifForm := url.Values{
		"telegram_bot_token":    {"token123"},
		"telegram_chat_id":      {"chat456"},
		"webhook_url":           {"https://example.com/webhook"},
		"notifications_enabled": {"on"},
	}
	req = httptest.NewRequest(http.MethodPost, "/settings/notifications", strings.NewReader(notifForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 on notification settings, got %d", rec.Code)
	}

	tokenSetting, _ := st.GetSetting("telegram_bot_token")
	if tokenSetting != "token123" {
		t.Errorf("expected token123, got %q", tokenSetting)
	}

	// 5. POST /settings/poller
	pollerForm := url.Values{
		"poller_enabled":  {"true"},
		"poller_interval": {"60"},
	}
	req = httptest.NewRequest(http.MethodPost, "/settings/poller", strings.NewReader(pollerForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 on poller settings, got %d", rec.Code)
	}

	intervalSetting, _ := st.GetSetting("poller_interval")
	if intervalSetting != "60" {
		t.Errorf("expected poller_interval 60, got %q", intervalSetting)
	}
}


