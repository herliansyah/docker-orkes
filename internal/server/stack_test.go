package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"docker-orkes/internal/server"
)

func TestServer_StackTemplatesAndValidation(t *testing.T) {
	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
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

	// 2. Attempt to create a Stack with invalid YAML -> Should fail
	invalidForm := url.Values{
		"name":         {"bad-stack"},
		"compose_yaml": {"services:\n  app:\n    image: [unclosed bracket"},
		"default_env":  {"FOO=bar"},
	}
	req = httptest.NewRequest(http.MethodPost, "/stacks", strings.NewReader(invalidForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for invalid YAML, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 2b. Attempt to create a Stack without 'services' key -> Should fail
	missingServicesForm := url.Values{
		"name":         {"no-services-stack"},
		"compose_yaml": {"version: '3.8'\nnetworks:\n  frontend:\n"},
		"default_env":  {"FOO=bar"},
	}
	req = httptest.NewRequest(http.MethodPost, "/stacks", strings.NewReader(missingServicesForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for missing services key, got %d", rec.Code)
	}

	// 3. Create a Stack with valid YAML
	validCompose := `services:
  web:
    image: nginx:alpine
    ports:
      - "80:80"
`
	validForm := url.Values{
		"name":         {"nginx-proxy"},
		"compose_yaml": {validCompose},
		"default_env":  {"PORT=80\nENV=prod"},
	}
	req = httptest.NewRequest(http.MethodPost, "/stacks", strings.NewReader(validForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after stack creation, got %d", rec.Code)
	}

	// 4. Verify stack is listed in /stacks
	req = httptest.NewRequest(http.MethodGet, "/stacks", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for /stacks, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "nginx-proxy") {
		t.Errorf("expected stack list to contain 'nginx-proxy'")
	}

	// 5. Verify Stack in store
	stacks, err := srv.Store().ListStacks()
	if err != nil || len(stacks) != 1 {
		t.Fatalf("expected 1 stack in store, got %d (err: %v)", len(stacks), err)
	}
	stackID := stacks[0].ID
	if stacks[0].Name != "nginx-proxy" {
		t.Errorf("expected stack name 'nginx-proxy', got %q", stacks[0].Name)
	}

	// 6. Update Stack compose and env
	updatedCompose := `services:
  web:
    image: nginx:latest
    ports:
      - "8080:80"
`
	updateForm := url.Values{
		"name":         {"nginx-proxy"},
		"compose_yaml": {updatedCompose},
		"default_env":  {"PORT=8080"},
	}
	req = httptest.NewRequest(http.MethodPost, "/stacks/"+stackID+"/update", strings.NewReader(updateForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after stack update, got %d", rec.Code)
	}

	updatedStack, err := srv.Store().GetStack(stackID)
	if err != nil {
		t.Fatalf("failed to fetch updated stack: %v", err)
	}
	if !strings.Contains(updatedStack.ComposeYAML, "nginx:latest") {
		t.Errorf("expected updated compose to contain 'nginx:latest', got %s", updatedStack.ComposeYAML)
	}

	// 7. Delete Stack
	req = httptest.NewRequest(http.MethodPost, "/stacks/"+stackID+"/delete", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after stack deletion, got %d", rec.Code)
	}

	stacks, err = srv.Store().ListStacks()
	if err != nil || len(stacks) != 0 {
		t.Errorf("expected 0 stacks after deletion, got %d", len(stacks))
	}
}
