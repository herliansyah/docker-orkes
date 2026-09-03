package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"docker-orkes/internal/server"
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

