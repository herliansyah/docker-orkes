package server

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"docker-orkes/internal/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

type Config struct {
	DBPath         string
	MasterPassword string
	Addr           string
}

type Server struct {
	cfg       Config
	store     *store.Store
	templates *template.Template
	pubKey    string
	privKey   string
}

func New(cfg Config) (*Server, error) {
	if cfg.MasterPassword == "" {
		cfg.MasterPassword = "admin" // ponytail: sensible default for zero-config quickstart, ceiling: user should provide strong password via env
	}

	st, err := store.New(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize store: %w", err)
	}

	pubKey, privKey, err := st.EnsureSSHKeyPair()
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("failed to ensure ssh keypair: %w", err)
	}

	tmpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		st.Close()
		return nil, fmt.Errorf("failed to parse templates: %w", err)
	}

	return &Server{
		cfg:       cfg,
		store:     st,
		templates: tmpl,
		pubKey:    pubKey,
		privKey:   privKey,
	}, nil
}

func (s *Server) Close() error {
	return s.store.Close()
}

func (s *Server) PublicKeySSH() string {
	return s.pubKey
}


func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public routes
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// Protected routes
	mux.Handle("GET /", s.requireAuth(http.HandlerFunc(s.handleDashboard)))

	return mux
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("orkes_session")
		if err != nil || !s.store.ValidateSession(cookie.Value) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	// If already authenticated, redirect to home
	if cookie, err := r.Cookie("orkes_session"); err == nil && s.store.ValidateSession(cookie.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "login.html", nil)
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	pass := r.FormValue("password")
	if pass != s.cfg.MasterPassword {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = s.templates.ExecuteTemplate(w, "login.html", map[string]string{
			"Error": "Invalid master password",
		})
		return
	}

	token := generateToken(32)
	if err := s.store.CreateSession(token); err != nil {
		http.Error(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "orkes_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(7 * 24 * time.Hour),
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("orkes_session"); err == nil {
		_ = s.store.DeleteSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "orkes_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := map[string]interface{}{
		"PublicKeySSH":    s.pubKey,
		"NodeCount":       0,
		"StackCount":      0,
		"DeploymentCount": 0,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "dashboard.html", data)
}

func generateToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
