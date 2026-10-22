package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

func TestServer_StaticAssetsAndTerminalAuth(t *testing.T) {
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// 1. Static asset: xterm.js
	req := httptest.NewRequest(http.MethodGet, "/static/xterm.js", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /static/xterm.js, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Terminal") {
		t.Errorf("expected xterm.js to contain Terminal")
	}

	// 2. Static asset: xterm.css
	req = httptest.NewRequest(http.MethodGet, "/static/xterm.css", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /static/xterm.css, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), ".xterm") {
		t.Errorf("expected xterm.css to contain .xterm")
	}

	// 3. Static asset: addon-fit.js
	req = httptest.NewRequest(http.MethodGet, "/static/addon-fit.js", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for /static/addon-fit.js, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "FitAddon") {
		t.Errorf("expected addon-fit.js to contain FitAddon")
	}

	// 4. WebSocket terminal unauthorized: standard request
	req = httptest.NewRequest(http.MethodGet, "/ws/terminal", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect for unauthenticated browser request, got %d", rec.Code)
	}

	// 5. WebSocket terminal unauthorized: websocket handshake
	req = httptest.NewRequest(http.MethodGet, "/ws/terminal", nil)
	req.Header.Set("Upgrade", "websocket")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for unauthenticated websocket upgrade, got %d", rec.Code)
	}
}

func TestServer_SecurityHardening(t *testing.T) {
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "test-password",
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// Log in to obtain valid auth cookie
	loginForm := url.Values{"password": {"test-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// 1. Rejection of invalid container_id in logs (injection prevention)
	invalidContainerIDs := []string{
		"c123;id",
		"c123$(whoami)",
		"c123|reboot",
		"c123&touch",
		"c 123",
		"c'123",
		"c\"123",
	}
	for _, cid := range invalidContainerIDs {
		req = httptest.NewRequest(http.MethodGet, "/nodes/node-1/containers/"+url.PathEscape(cid)+"/logs", nil)
		req.AddCookie(cookie)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for container ID %q, got %d", cid, rec.Code)
		}
	}

	// 2. Rejection of invalid stack slug names
	invalidStackNames := []string{
		"-leading-dash",
		".leading-dot",
		"has spaces",
		"has;semicolon",
		"has$dollar",
		"has`backtick`",
		"has/slash",
	}
	for _, sname := range invalidStackNames {
		form := url.Values{
			"name":         {sname},
			"compose_yaml": {"services:\n  app:\n    image: nginx:alpine\n"},
		}
		req = httptest.NewRequest(http.MethodPost, "/stacks", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for stack name %q, got %d", sname, rec.Code)
		}
	}

	// 3. Rejection of invalid git branch / repoURL settings
	invalidGitBranches := []string{
		"-b",
		"--upload-pack=evil",
		"has space",
	}
	for _, branch := range invalidGitBranches {
		form := url.Values{
			"git_mirror_url":    {"https://github.com/example/repo.git"},
			"git_mirror_branch": {branch},
		}
		req = httptest.NewRequest(http.MethodPost, "/settings/git", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for git branch %q, got %d", branch, rec.Code)
		}
	}

	// 4. Session 7-day TTL expiration
	st := srv.Store()
	// Insert old session token directly with timestamp 8 days ago
	oldToken := "old-expired-session-token"
	_, err = st.DB().Exec("INSERT INTO sessions (token, created_at) VALUES (?, datetime('now', '-8 days'))", oldToken)
	if err != nil {
		t.Fatalf("failed to insert expired session: %v", err)
	}
	if st.ValidateSession(oldToken) {
		t.Errorf("expected expired session (8 days old) to be invalid")
	}

	// Fresh session should be valid
	freshToken := "fresh-valid-session-token"
	_ = st.CreateSession(freshToken)
	if !st.ValidateSession(freshToken) {
		t.Errorf("expected fresh session to be valid")
	}
}

func TestServer_LoginRateLimiting(t *testing.T) {
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "super-secret-password",
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// Perform 5 consecutive failed login attempts from same IP
	for i := 1; i <= 5; i++ {
		formData := url.Values{"password": {"wrong"}}
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "192.168.1.50:12345"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401 Unauthorized, got %d", i, rec.Code)
		}
	}

	// 6th attempt should be blocked by rate limiter (429 Too Many Requests)
	formData := url.Values{"password": {"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.168.1.50:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt 6: expected 429 Too Many Requests, got %d", rec.Code)
	}

	// A different IP should NOT be blocked
	reqDiff := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(formData.Encode()))
	reqDiff.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqDiff.RemoteAddr = "192.168.1.51:12345"
	recDiff := httptest.NewRecorder()
	handler.ServeHTTP(recDiff, reqDiff)

	if recDiff.Code != http.StatusUnauthorized {
		t.Fatalf("different IP: expected 401 Unauthorized, got %d", recDiff.Code)
	}
}

func TestServer_Healthz(t *testing.T) {
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /healthz, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected body from /healthz: %s", rec.Body.String())
	}
}

func TestServer_NodeNameValidationAndLogsSecurity(t *testing.T) {
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
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

	// 1. Invalid node names with path traversal must be rejected
	for _, badName := range []string{"../escape", "prod/worker", "test\\worker", "..", "a/b/c"} {
		nodeForm := url.Values{
			"name": {badName},
			"host": {"10.0.0.1"},
		}
		req = httptest.NewRequest(http.MethodPost, "/nodes", strings.NewReader(nodeForm.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for node name %q, got %d", badName, rec.Code)
		}
	}

	// 2. Container logs with hyphen must be rejected
	reqLogs := httptest.NewRequest(http.MethodGet, "/nodes/n1/containers/-f/logs", nil)
	reqLogs.AddCookie(cookie)
	recLogs := httptest.NewRecorder()
	handler.ServeHTTP(recLogs, reqLogs)
	if recLogs.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for container ID starting with hyphen, got %d", recLogs.Code)
	}
}

func TestServer_DatabaseBackup(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "orkes_backup_test.db")
	srv, err := server.New(server.Config{
		DBPath:         tempDB,
		MasterPassword: "secret-password",
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	st := srv.Store()
	sessionToken := "backup-test-session"
	_ = st.CreateSession(sessionToken)
	sessionCookie := &http.Cookie{Name: "orkes_session", Value: sessionToken}

	_ = st.CreateNode(store.Node{ID: "node-1", Name: "prod-node", Host: "10.0.0.1", Port: 22, User: "root"})

	handler := srv.Handler()

	// 1. Unauthenticated request to /api/backup should be rejected with 401 Unauthorized
	reqUnauth := httptest.NewRequest(http.MethodGet, "/api/backup", nil)
	recUnauth := httptest.NewRecorder()
	handler.ServeHTTP(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for unauthenticated backup request, got %d", recUnauth.Code)
	}

	// 2. Authenticated request to /api/backup
	reqAuth := httptest.NewRequest(http.MethodGet, "/api/backup", nil)
	reqAuth.AddCookie(sessionCookie)
	recAuth := httptest.NewRecorder()
	handler.ServeHTTP(recAuth, reqAuth)

	if recAuth.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /api/backup, got %d", recAuth.Code)
	}
	if ct := recAuth.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("expected application/octet-stream Content-Type, got %q", ct)
	}
	cd := recAuth.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, "docker-orkes-backup-") {
		t.Errorf("expected Content-Disposition attachment with backup filename, got %q", cd)
	}

	backupBytes := recAuth.Body.Bytes()
	if len(backupBytes) < 16 {
		t.Fatalf("backup file too small: %d bytes", len(backupBytes))
	}
	if !bytes.HasPrefix(backupBytes, []byte("SQLite format 3\x00")) {
		t.Errorf("expected SQLite header magic bytes, got %q", backupBytes[:16])
	}
}

func TestServer_ContainerActions(t *testing.T) {
	exec := &mockDeployExecutor{}
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
		Executor:       exec,
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	st := srv.Store()
	sessionToken := "container-action-session"
	_ = st.CreateSession(sessionToken)
	sessionCookie := &http.Cookie{Name: "orkes_session", Value: sessionToken}

	_ = st.CreateNode(store.Node{ID: "n1", Name: "node-1", Host: "10.0.0.1", Port: 22, User: "root"})
	handler := srv.Handler()

	// 1. Start Container
	reqStart := httptest.NewRequest(http.MethodPost, "/nodes/n1/containers/c-app-1/start", nil)
	reqStart.AddCookie(sessionCookie)
	recStart := httptest.NewRecorder()
	handler.ServeHTTP(recStart, reqStart)

	if recStart.Code != http.StatusOK {
		t.Fatalf("expected 200 for start container, got %d: %s", recStart.Code, recStart.Body.String())
	}
	var resStart map[string]interface{}
	if err := json.Unmarshal(recStart.Body.Bytes(), &resStart); err != nil || resStart["success"] != true {
		t.Errorf("expected JSON success:true, got %+v", resStart)
	}

	// 2. Stop Container
	reqStop := httptest.NewRequest(http.MethodPost, "/nodes/n1/containers/c-app-1/stop", nil)
	reqStop.AddCookie(sessionCookie)
	recStop := httptest.NewRecorder()
	handler.ServeHTTP(recStop, reqStop)

	if recStop.Code != http.StatusOK {
		t.Fatalf("expected 200 for stop container, got %d: %s", recStop.Code, recStop.Body.String())
	}
	var resStop map[string]interface{}
	if err := json.Unmarshal(recStop.Body.Bytes(), &resStop); err != nil || resStop["success"] != true {
		t.Errorf("expected JSON success:true, got %+v", resStop)
	}

	// 3. Restart Container
	reqRestart := httptest.NewRequest(http.MethodPost, "/nodes/n1/containers/c-app-1/restart", nil)
	reqRestart.AddCookie(sessionCookie)
	recRestart := httptest.NewRecorder()
	handler.ServeHTTP(recRestart, reqRestart)

	if recRestart.Code != http.StatusOK {
		t.Fatalf("expected 200 for restart container, got %d: %s", recRestart.Code, recRestart.Body.String())
	}
	var resRestart map[string]interface{}
	if err := json.Unmarshal(recRestart.Body.Bytes(), &resRestart); err != nil || resRestart["success"] != true {
		t.Errorf("expected JSON success:true, got %+v", resRestart)
	}

	// 4. Verify executed commands
	exec.mu.Lock()
	cmds := append([]string{}, exec.executedCmds...)
	exec.mu.Unlock()

	foundStart, foundStop, foundRestart := false, false, false
	for _, c := range cmds {
		if strings.Contains(c, "docker start -- c-app-1") {
			foundStart = true
		}
		if strings.Contains(c, "docker stop -- c-app-1") {
			foundStop = true
		}
		if strings.Contains(c, "docker restart -- c-app-1") {
			foundRestart = true
		}
	}
	if !foundStart {
		t.Errorf("expected docker start command, got %v", cmds)
	}
	if !foundStop {
		t.Errorf("expected docker stop command, got %v", cmds)
	}
	if !foundRestart {
		t.Errorf("expected docker restart command, got %v", cmds)
	}

	// 5. Invalid container ID should fail validation
	reqInvalid := httptest.NewRequest(http.MethodPost, "/nodes/n1/containers/-bad-id/start", nil)
	reqInvalid.AddCookie(sessionCookie)
	recInvalid := httptest.NewRecorder()
	handler.ServeHTTP(recInvalid, reqInvalid)
	if recInvalid.Code == http.StatusOK {
		t.Errorf("expected non-200 status for invalid container ID, got %d", recInvalid.Code)
	}
}

func TestServer_ContainerLogsStreaming(t *testing.T) {
	exec := &mockDeployExecutor{}
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
		Executor:       exec,
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	st := srv.Store()
	sessionToken := "logs-streaming-session"
	_ = st.CreateSession(sessionToken)
	sessionCookie := &http.Cookie{Name: "orkes_session", Value: sessionToken}

	_ = st.CreateNode(store.Node{ID: "n1", Name: "node-1", Host: "10.0.0.1", Port: 22, User: "root"})
	handler := srv.Handler()

	// 1. Static logs fetch
	reqStatic := httptest.NewRequest(http.MethodGet, "/nodes/n1/containers/c-app-1/logs?tail=50", nil)
	reqStatic.AddCookie(sessionCookie)
	recStatic := httptest.NewRecorder()
	handler.ServeHTTP(recStatic, reqStatic)

	if recStatic.Code != http.StatusOK {
		t.Fatalf("expected 200 for static logs, got %d", recStatic.Code)
	}

	// 2. Streaming logs fetch (?follow=true)
	reqStream := httptest.NewRequest(http.MethodGet, "/nodes/n1/containers/c-app-1/logs?follow=true&tail=50", nil)
	reqStream.AddCookie(sessionCookie)
	recStream := httptest.NewRecorder()
	handler.ServeHTTP(recStream, reqStream)

	if recStream.Code != http.StatusOK {
		t.Fatalf("expected 200 for streaming logs, got %d", recStream.Code)
	}
	if recStream.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("expected Cache-Control no-cache for streaming logs, got %q", recStream.Header().Get("Cache-Control"))
	}
	if recStream.Header().Get("X-Accel-Buffering") != "no" {
		t.Errorf("expected X-Accel-Buffering no for streaming logs, got %q", recStream.Header().Get("X-Accel-Buffering"))
	}

	body := recStream.Body.String()
	if !strings.Contains(body, "Container web Started") {
		t.Errorf("expected streaming body from mock stream, got %q", body)
	}

	// Check commands
	exec.mu.Lock()
	cmds := append([]string{}, exec.executedCmds...)
	exec.mu.Unlock()

	foundStreamCmd := false
	for _, c := range cmds {
		if strings.Contains(c, "docker logs -f --tail 50 -- c-app-1") {
			foundStreamCmd = true
			break
		}
	}
	if !foundStreamCmd {
		t.Errorf("expected docker logs -f command, got %v", cmds)
	}
}

func TestServer_APITokenAuth(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "orkes_token_test.db")
	srv, err := server.New(server.Config{
		DBPath:         tempDB,
		MasterPassword: "secret-password",
		APIToken:       "ci-secret-token-12345",
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// 1. Request with Bearer token should succeed
	reqBearer := httptest.NewRequest(http.MethodGet, "/nodes", nil)
	reqBearer.Header.Set("Authorization", "Bearer ci-secret-token-12345")
	recBearer := httptest.NewRecorder()
	handler.ServeHTTP(recBearer, reqBearer)

	if recBearer.Code != http.StatusOK {
		t.Errorf("expected 200 OK with Bearer token, got %d", recBearer.Code)
	}

	// 2. Request with X-API-Token header should succeed
	reqCustom := httptest.NewRequest(http.MethodGet, "/nodes", nil)
	reqCustom.Header.Set("X-API-Token", "ci-secret-token-12345")
	recCustom := httptest.NewRecorder()
	handler.ServeHTTP(recCustom, reqCustom)

	if recCustom.Code != http.StatusOK {
		t.Errorf("expected 200 OK with X-API-Token header, got %d", recCustom.Code)
	}

	// 3. Request with wrong token should fail with 401
	reqWrong := httptest.NewRequest(http.MethodGet, "/nodes", nil)
	reqWrong.Header.Set("Authorization", "Bearer invalid-token")
	reqWrong.Header.Set("Accept", "application/json")
	recWrong := httptest.NewRecorder()
	handler.ServeHTTP(recWrong, reqWrong)

	if recWrong.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for wrong API token, got %d", recWrong.Code)
	}
}
