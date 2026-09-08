package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"sync"

	"docker-orkes/internal/controlplane"
	"docker-orkes/internal/drift"
	"docker-orkes/internal/gitsync"
	"docker-orkes/internal/notifier"
	"docker-orkes/internal/nodeclient"
	"docker-orkes/internal/poller"
	"docker-orkes/internal/ssh"
	"docker-orkes/internal/store"

	"golang.org/x/net/websocket"
	"gopkg.in/yaml.v3"
)

//go:embed templates/*.html templates/static/*
var templatesFS embed.FS

type Config struct {
	DBPath         string
	MasterPassword string
	Addr           string
	Executor       ssh.Executor
	GitSyncer      gitsync.Syncer
	GitMirrorDir   string
}

type Server struct {
	cfg          Config
	store        *store.Store
	cp           controlplane.Engine
	executor     ssh.Executor
	gitSyncer    gitsync.Syncer
	gitMirrorDir string
	templates    *template.Template
	pubKey       string
	privKey      string
	notifier     notifier.Notifier
	poller       poller.Poller
	mu           sync.RWMutex
}

func New(cfg Config) (*Server, error) {
	if cfg.MasterPassword == "" {
		cfg.MasterPassword = "admin" // ponytail: sensible default for zero-config quickstart, ceiling: user should provide strong password via env
	}
	if cfg.Executor == nil {
		cfg.Executor = ssh.NewExecutor()
	}
	if cfg.GitSyncer == nil {
		cfg.GitSyncer = gitsync.NewSyncer()
	}
	if cfg.GitMirrorDir == "" {
		cfg.GitMirrorDir = "data/git-mirror"
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

	cp := controlplane.New(controlplane.Config{
		Store:        st,
		Executor:     cfg.Executor,
		GitSyncer:    cfg.GitSyncer,
		GitMirrorDir: cfg.GitMirrorDir,
		PrivKeyPEM:   privKey,
	})

	// Notifications
	notifEnabled, _ := st.GetSetting("notifications_enabled")
	telegramToken, _ := st.GetSetting("telegram_bot_token")
	telegramChatID, _ := st.GetSetting("telegram_chat_id")
	webhookURL, _ := st.GetSetting("webhook_url")

	notif := notifier.New(notifier.Config{
		TelegramBotToken: telegramToken,
		TelegramChatID:   telegramChatID,
		WebhookURL:       webhookURL,
		Enabled:          notifEnabled == "true",
	})

	// Background Poller (ADR 0004)
	pollerEnabled, _ := st.GetSetting("poller_enabled")
	pollerIntervalStr, _ := st.GetSetting("poller_interval")
	pollerInterval := poller.ParseIntervalOrDefault(pollerIntervalStr, 2*time.Minute)

	plr := poller.New(poller.Config{
		Engine:   cp,
		Notifier: notif,
		Interval: pollerInterval,
	})
	if pollerEnabled != "false" {
		plr.Start()
	}

	return &Server{
		cfg:          cfg,
		store:        st,
		cp:           cp,
		executor:     cfg.Executor,
		gitSyncer:    cfg.GitSyncer,
		gitMirrorDir: cfg.GitMirrorDir,
		templates:    tmpl,
		pubKey:       pubKey,
		privKey:      privKey,
		notifier:     notif,
		poller:       plr,
	}, nil
}

func (s *Server) ControlPlane() controlplane.Engine {
	return s.cp
}

func (s *Server) Store() *store.Store {
	return s.store
}

func (s *Server) Close() error {
	s.mu.Lock()
	if s.poller != nil {
		s.poller.Stop()
	}
	s.mu.Unlock()
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
	mux.Handle("GET /nodes", s.requireAuth(http.HandlerFunc(s.handleListNodes)))
	mux.Handle("POST /nodes", s.requireAuth(http.HandlerFunc(s.handleCreateNode)))
	mux.Handle("POST /nodes/{id}/test", s.requireAuth(http.HandlerFunc(s.handleTestNode)))
	mux.Handle("POST /nodes/{id}/bootstrap", s.requireAuth(http.HandlerFunc(s.handleBootstrapNode)))
	mux.Handle("POST /nodes/{id}/update", s.requireAuth(http.HandlerFunc(s.handleUpdateNode)))
	mux.Handle("POST /nodes/{id}/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteNode)))
	mux.Handle("GET /nodes/{id}", s.requireAuth(http.HandlerFunc(s.handleNodeDetail)))
	mux.Handle("GET /nodes/{id}/telemetry", s.requireAuth(http.HandlerFunc(s.handleNodeTelemetry)))
	mux.Handle("GET /nodes/{id}/images", s.requireAuth(http.HandlerFunc(s.handleListNodeImages)))
	mux.Handle("POST /nodes/{id}/images/pin", s.requireAuth(http.HandlerFunc(s.handlePinNodeImage)))
	mux.Handle("POST /nodes/{id}/images/pull", s.requireAuth(http.HandlerFunc(s.handlePullNodeImage)))
	mux.Handle("POST /nodes/{id}/images/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteNodeImage)))
	mux.Handle("GET /nodes/{id}/volumes", s.requireAuth(http.HandlerFunc(s.handleListNodeVolumes)))
	mux.Handle("POST /nodes/{id}/volumes/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteNodeVolume)))
	mux.Handle("POST /nodes/{id}/volumes/prune", s.requireAuth(http.HandlerFunc(s.handlePruneNodeVolumes)))
	mux.Handle("GET /nodes/{id}/networks", s.requireAuth(http.HandlerFunc(s.handleListNodeNetworks)))
	mux.Handle("POST /nodes/{id}/networks/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteNodeNetwork)))
	mux.Handle("POST /nodes/{id}/networks/prune", s.requireAuth(http.HandlerFunc(s.handlePruneNodeNetworks)))
	mux.Handle("GET /nodes/{id}/containers/{container_id}/logs", s.requireAuth(http.HandlerFunc(s.handleContainerLogs)))

	mux.Handle("GET /stacks", s.requireAuth(http.HandlerFunc(s.handleListStacks)))
	mux.Handle("POST /stacks", s.requireAuth(http.HandlerFunc(s.handleCreateStack)))
	mux.Handle("POST /stacks/{id}/update", s.requireAuth(http.HandlerFunc(s.handleUpdateStack)))
	mux.Handle("POST /stacks/{id}/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteStack)))

	mux.Handle("GET /deployments", s.requireAuth(http.HandlerFunc(s.handleListDeployments)))
	mux.Handle("POST /deployments", s.requireAuth(http.HandlerFunc(s.handleCreateDeployment)))
	mux.Handle("POST /deployments/{id}/update", s.requireAuth(http.HandlerFunc(s.handleUpdateDeployment)))
	mux.Handle("POST /deployments/{id}/up", s.requireAuth(http.HandlerFunc(s.handleUpDeployment)))
	mux.Handle("POST /deployments/{id}/rollout", s.requireAuth(http.HandlerFunc(s.handleRolloutDeployment)))
	mux.Handle("POST /deployments/{id}/restart", s.requireAuth(http.HandlerFunc(s.handleRestartDeployment)))
	mux.Handle("POST /deployments/{id}/down", s.requireAuth(http.HandlerFunc(s.handleDownDeployment)))
	mux.Handle("POST /deployments/{id}/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteDeployment)))
	mux.Handle("GET /deployments/{id}/containers", s.requireAuth(http.HandlerFunc(s.handleGetDeploymentContainers)))
	mux.Handle("GET /deployments/{id}/preflight", s.requireAuth(http.HandlerFunc(s.handleDeploymentPreflight)))


	mux.Handle("POST /deployments/{id}/drift/check", s.requireAuth(http.HandlerFunc(s.handleCheckDrift)))
	mux.Handle("POST /deployments/{id}/drift/reconcile", s.requireAuth(http.HandlerFunc(s.handleReconcileDrift)))
	mux.Handle("POST /deployments/{id}/drift/adopt", s.requireAuth(http.HandlerFunc(s.handleAdoptDrift)))
	mux.Handle("GET /deployments/{id}/files", s.requireAuth(http.HandlerFunc(s.handleListDeploymentFiles)))
	mux.Handle("GET /deployments/{id}/files/view", s.requireAuth(http.HandlerFunc(s.handleViewDeploymentFile)))
	mux.Handle("POST /deployments/{id}/files/save", s.requireAuth(http.HandlerFunc(s.handleSaveDeploymentFile)))
	mux.Handle("POST /deployments/{id}/files/mkdir", s.requireAuth(http.HandlerFunc(s.handleCreateDeploymentDir)))
	mux.Handle("POST /deployments/{id}/files/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteDeploymentFile)))
	mux.Handle("POST /deployments/{id}/files/extract", s.requireAuth(http.HandlerFunc(s.handleExtractDeploymentArchive)))
	mux.Handle("POST /deployments/{id}/files/upload", s.requireAuth(http.HandlerFunc(s.handleUploadDeploymentFiles)))



	mux.Handle("GET /deployments/{id}/revisions", s.requireAuth(http.HandlerFunc(s.handleListRevisions)))
	mux.Handle("POST /deployments/{id}/rollback", s.requireAuth(http.HandlerFunc(s.handleRollbackDeployment)))

	mux.Handle("POST /settings/git", s.requireAuth(http.HandlerFunc(s.handleUpdateGitSettings)))
	mux.Handle("POST /settings/git/sync", s.requireAuth(http.HandlerFunc(s.handleSyncGitNow)))
	mux.Handle("POST /settings/runbook", s.requireAuth(http.HandlerFunc(s.handleUpdateRunbook)))
	mux.Handle("POST /settings/notifications", s.requireAuth(http.HandlerFunc(s.handleUpdateNotificationSettings)))
	mux.Handle("POST /settings/notifications/test", s.requireAuth(http.HandlerFunc(s.handleTestNotification)))
	mux.Handle("POST /settings/poller", s.requireAuth(http.HandlerFunc(s.handleUpdatePollerSettings)))

	// Static embedded assets (xterm, etc.)
	staticSub, err := fs.Sub(templatesFS, "templates/static")
	if err == nil {
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	}

	// Web Terminal WebSocket
	mux.Handle("GET /ws/terminal", s.requireAuth(websocket.Handler(s.handleTerminalWebSocket)))

	return mux
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("orkes_session")
		if err != nil || !s.store.ValidateSession(cookie.Value) {
			if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
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

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	s.handleDashboard(w, r)
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	host := strings.TrimSpace(r.FormValue("host"))
	portStr := strings.TrimSpace(r.FormValue("port"))
	user := strings.TrimSpace(r.FormValue("user"))
	notes := strings.TrimSpace(r.FormValue("notes"))

	if name == "" || host == "" {
		http.Error(w, "Name and host are required", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 22
	}
	if user == "" {
		user = "root"
	}

	sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))

	node := store.Node{
		ID:        generateToken(8),
		Name:      name,
		Host:      host,
		Port:      port,
		User:      user,
		Notes:     notes,
		Status:    "unknown",
		CreatedAt: time.Now().UTC(),
	}


	if sshPassword != "" {
		if err := s.executor.BootstrapKey(r.Context(), node.Host, node.Port, node.User, sshPassword, s.pubKey); err != nil {
			node.Status = "offline"
			node.LastError = "Auto-inject key failed: " + err.Error()
		} else {
			dockerVer, err := s.executor.TestConnection(r.Context(), node.Host, node.Port, node.User, s.privKey)
			if err != nil {
				node.Status = "offline"
				node.LastError = err.Error()
			} else {
				node.Status = "online"
				node.DockerVersion = dockerVer
			}
		}
	}

	if err := s.store.CreateNode(node); err != nil {
		http.Error(w, "Failed to create node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleBootstrapNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
	if sshPassword == "" {
		http.Error(w, "SSH Password is required", http.StatusBadRequest)
		return
	}

	_ = s.cp.BootstrapNode(r.Context(), id, sshPassword, s.pubKey)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleTestNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dockerVer, err := s.cp.TestNode(r.Context(), id)
	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":        true,
			"docker_version": dockerVer,
		})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteNode(id); err != nil {
		http.Error(w, "Failed to delete node: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	host := strings.TrimSpace(r.FormValue("host"))
	portStr := strings.TrimSpace(r.FormValue("port"))
	user := strings.TrimSpace(r.FormValue("user"))
	notes := strings.TrimSpace(r.FormValue("notes"))

	if name == "" || host == "" {
		http.Error(w, "Name and host are required", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 22
	}
	if user == "" {
		user = "root"
	}

	node.Name = name
	node.Host = host
	node.Port = port
	node.User = user
	node.Notes = notes

	if err := s.store.UpdateNode(*node); err != nil {

		http.Error(w, "Failed to update node: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = s.triggerGitSync(r.Context())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}


func (s *Server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	s.handleDashboard(w, r)
}

func (s *Server) handleCreateStack(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	composeYAML := strings.TrimSpace(r.FormValue("compose_yaml"))
	defaultEnv := strings.TrimSpace(r.FormValue("default_env"))
	notes := strings.TrimSpace(r.FormValue("notes"))

	if name == "" || composeYAML == "" {
		http.Error(w, "Name and compose_yaml are required", http.StatusBadRequest)
		return
	}

	if err := validateStackSlug(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validateComposeYAML(composeYAML); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	stack := store.Stack{
		ID:          generateToken(8),
		Name:        name,
		ComposeYAML: composeYAML,
		DefaultEnv:  defaultEnv,
		Notes:       notes,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.store.CreateStack(stack); err != nil {
		http.Error(w, "Failed to create stack: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleUpdateStack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	composeYAML := strings.TrimSpace(r.FormValue("compose_yaml"))
	defaultEnv := strings.TrimSpace(r.FormValue("default_env"))
	notes := strings.TrimSpace(r.FormValue("notes"))

	if name == "" || composeYAML == "" {
		http.Error(w, "Name and compose_yaml are required", http.StatusBadRequest)
		return
	}

	if err := validateStackSlug(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := validateComposeYAML(composeYAML); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	stack := store.Stack{
		ID:          id,
		Name:        name,
		ComposeYAML: composeYAML,
		DefaultEnv:  defaultEnv,
		Notes:       notes,
		UpdatedAt:   time.Now().UTC(),
	}


	if err := s.store.UpdateStack(stack); err != nil {
		http.Error(w, "Failed to update stack: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDeleteStack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteStack(id); err != nil {
		http.Error(w, "Failed to delete stack: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func validateComposeYAML(content string) error {
	var parsed map[string]interface{}
	if err := yaml.Unmarshal([]byte(content), &parsed); err != nil {
		return fmt.Errorf("invalid YAML syntax: %w", err)
	}
	if len(parsed) == 0 {
		return fmt.Errorf("compose YAML cannot be empty")
	}
	return nil
}
var stackSlugRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var gitBranchRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,99}$`)

func validateStackSlug(name string) error {
	if !stackSlugRegex.MatchString(name) {
		return errors.New("invalid stack name: must be 1-64 characters alphanumeric, dashes, dots, or underscores, starting with alphanumeric")
	}
	return nil
}


func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	s.handleDashboard(w, r)
}

func (s *Server) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	stackID := strings.TrimSpace(r.FormValue("stack_id"))
	nodeID := strings.TrimSpace(r.FormValue("node_id"))
	envVars := strings.TrimSpace(r.FormValue("env_vars"))
	notes := strings.TrimSpace(r.FormValue("notes"))

	if stackID == "" || nodeID == "" {
		http.Error(w, "stack_id and node_id are required", http.StatusBadRequest)
		return
	}

	dep := store.Deployment{
		ID:        generateToken(8),
		StackID:   stackID,
		NodeID:    nodeID,
		EnvVars:   envVars,
		Notes:     notes,
		Status:    "deploying",
		UpdatedAt: time.Now().UTC(),
	}

	if err := s.store.CreateDeployment(dep); err != nil {
		http.Error(w, "Failed to create deployment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = s.deployStack(r.Context(), &dep)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleUpdateDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	envVars := strings.TrimSpace(r.FormValue("env_vars"))
	notes := strings.TrimSpace(r.FormValue("notes"))
	if err := s.store.UpdateDeploymentEnv(dep.ID, envVars, notes); err != nil {
		http.Error(w, "Failed to update deployment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = s.triggerGitSync(r.Context())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}



func (s *Server) handleDeploymentPreflight(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	preflight, err := s.cp.GetDeploymentPreflight(r.Context(), id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"preflight": preflight,
	})
}

func (s *Server) handleUpDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.cp.Deploy(r.Context(), id)
	dep, _ := s.store.GetDeployment(id)
	lastLog := ""
	if dep != nil {
		lastLog = dep.LastLog
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") || strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
				"output":  lastLog,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"output":  lastLog,
			"message": "Stack deployed successfully and containers are running",
		})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleRolloutDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.cp.Deploy(r.Context(), id)
	dep, _ := s.store.GetDeployment(id)
	lastLog := ""
	if dep != nil {
		lastLog = dep.LastLog
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") || strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
				"output":  lastLog,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"output":  lastLog,
			"message": "Images pulled and stack rolled out successfully",
		})
		return
	}
	if err != nil {
		http.Error(w, "Rollout failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleRestartDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.cp.Restart(r.Context(), id)
	dep, _ := s.store.GetDeployment(id)
	lastLog := ""
	if dep != nil {
		lastLog = dep.LastLog
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") || strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
				"output":  lastLog,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"output":  lastLog,
			"message": "Stack containers restarted successfully",
		})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDownDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.cp.Down(r.Context(), id)
	dep, _ := s.store.GetDeployment(id)
	lastLog := ""
	if dep != nil {
		lastLog = dep.LastLog
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") || strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
				"output":  lastLog,
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"output":  lastLog,
			"message": "Stack stopped and containers removed (volumes preserved)",
		})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}

	var action string = "teardown"
	var purgeVolumes bool
	var confirmName string
	var force bool

	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var payload struct {
			Action       string `json:"action"`
			PurgeVolumes bool   `json:"purge_volumes"`
			ConfirmName  string `json:"confirm_name"`
			Force        bool   `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			if payload.Action != "" {
				action = payload.Action
			}
			purgeVolumes = payload.PurgeVolumes
			confirmName = payload.ConfirmName
			force = payload.Force
		}
	} else {
		_ = r.ParseMultipartForm(32 << 20)
		if r.Form == nil {
			_ = r.ParseForm()
		}
		if a := r.FormValue("action"); a != "" {
			action = a
		}
		pv := strings.ToLower(r.FormValue("purge_volumes"))
		purgeVolumes = pv == "true" || pv == "1" || pv == "on"
		confirmName = r.FormValue("confirm_name")
		fc := strings.ToLower(r.FormValue("force"))
		force = fc == "true" || fc == "1" || fc == "on"
	}

	if purgeVolumes && confirmName != dep.StackName {
		http.Error(w, "Typed confirmation does not match stack name", http.StatusBadRequest)
		return
	}

	teardown := action != "detach"
	if err := s.cp.DeleteDeployment(r.Context(), id, teardown, purgeVolumes, force); err != nil {
		http.Error(w, "Failed to delete deployment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") || strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) deployStack(ctx context.Context, dep *store.Deployment) error {
	return s.cp.Deploy(ctx, dep.ID)
}

func computeDesiredHash(composeYAML, envVars string) string {
	return drift.ComputeHash(composeYAML, envVars)
}

func (s *Server) handleCheckDrift(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := s.cp.CheckDrift(r.Context(), id)

	isJSON := strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest"
	if isJSON {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		driftStatus := "synced"
		diff := ""
		isDrifted := false
		if result != nil {
			isDrifted = result.IsDrifted
			diff = result.Diff
			if isDrifted {
				driftStatus = "drifted"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":       true,
			"deployment_id": id,
			"is_drifted":    isDrifted,
			"drift_status":  driftStatus,
			"diff":          diff,
		})
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleReconcileDrift(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cp.ReconcileDrift(r.Context(), id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleAdoptDrift(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cp.AdoptDrift(r.Context(), id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleListDeploymentFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	subpath := strings.TrimSpace(r.URL.Query().Get("path"))
	remoteDir, files, err := s.cp.ListDeploymentFiles(r.Context(), id, subpath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":      err.Error(),
			"remote_dir": remoteDir,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"remote_dir": remoteDir,
		"subpath":    subpath,
		"files":      files,
	})
}

func (s *Server) handleViewDeploymentFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fileName := strings.TrimSpace(r.URL.Query().Get("file"))
	if fileName == "" || strings.Contains(fileName, "..") || strings.HasPrefix(fileName, "/") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	remoteFilePath, content, err := s.cp.ViewDeploymentFile(r.Context(), id, fileName)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"file":    fileName,
		"path":    remoteFilePath,
		"content": string(content),
	})
}

func (s *Server) handleSaveDeploymentFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	fileName := strings.TrimSpace(r.FormValue("file"))
	content := r.FormValue("content")
	redeploy := r.FormValue("redeploy") == "true"

	if fileName == "" || strings.Contains(fileName, "..") || strings.HasPrefix(fileName, "/") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	saveRes, err := s.cp.SaveDeploymentFile(r.Context(), id, fileName, []byte(content), redeploy)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Failed to upload file to remote server: " + err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":      true,
		"file":         saveRes.FileName,
		"redeploy":     saveRes.Redeploy,
		"redeploy_err": saveRes.RedeployErr,
		"output":       saveRes.Output,
		"drift_status": saveRes.DriftStatus,
	})
}

func (s *Server) handleCreateDeploymentDir(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	dirPath := strings.TrimSpace(r.FormValue("path"))
	if dirPath == "" || strings.Contains(dirPath, "..") || strings.HasPrefix(dirPath, "/") {
		http.Error(w, "Invalid directory path", http.StatusBadRequest)
		return
	}

	if err := s.cp.CreateDeploymentDir(r.Context(), id, dirPath); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"path":    dirPath,
	})
}

func (s *Server) handleDeleteDeploymentFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	filePath := strings.TrimSpace(r.FormValue("path"))
	if filePath == "" || strings.Contains(filePath, "..") || strings.HasPrefix(filePath, "/") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	if filePath == "docker-compose.yml" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "cannot delete docker-compose.yml: core deployment file is protected",
		})
		return
	}

	if err := s.cp.DeleteDeploymentPath(r.Context(), id, filePath); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"path":    filePath,
	})
}

func (s *Server) handleGetDeploymentContainers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}

	node, err := s.store.GetNode(dep.NodeID)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	psOut, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker ps -a --format '{{json .}}'")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":      fmt.Sprintf("Failed to query containers on node: %v", err),
			"node_id":    node.ID,
			"node_name":  node.Name,
			"stack_name": dep.StackName,
			"containers": []ContainerInfo{},
		})
		return
	}

	knownStacks := map[string]bool{dep.StackName: true}
	allContainers := parseDockerPsOutput(psOut, knownStacks)

	var matching []ContainerInfo
	stackLower := strings.ToLower(dep.StackName)
	for _, c := range allContainers {
		if strings.EqualFold(c.StackName, dep.StackName) || strings.Contains(strings.ToLower(c.Names), stackLower) {
			matching = append(matching, c)
		}
	}
	if matching == nil {
		matching = []ContainerInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"node_id":    node.ID,
		"node_name":  node.Name,
		"stack_name": dep.StackName,
		"containers": matching,
	})
}

func (s *Server) handleExtractDeploymentArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	archiveFile := strings.TrimSpace(r.FormValue("file"))
	targetDir := strings.TrimSpace(r.FormValue("target_dir"))
	if archiveFile == "" || strings.Contains(archiveFile, "..") || strings.HasPrefix(archiveFile, "/") {
		http.Error(w, "Invalid archive filename", http.StatusBadRequest)
		return
	}
	if targetDir != "" && (strings.Contains(targetDir, "..") || strings.HasPrefix(targetDir, "/")) {
		http.Error(w, "Invalid target directory", http.StatusBadRequest)
		return
	}

	if err := s.cp.ExtractDeploymentArchive(r.Context(), id, archiveFile, targetDir); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":    true,
		"file":       archiveFile,
		"target_dir": targetDir,
	})
}

func (s *Server) handleUploadDeploymentFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// ponytail: Limit upload batch to 100MB as agreed in architecture decision
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "Request too large or invalid multipart form (max 100MB)", http.StatusBadRequest)
		return
	}

	prefixDir := strings.TrimSpace(r.FormValue("subpath"))
	if prefixDir != "" && (strings.Contains(prefixDir, "..") || strings.HasPrefix(prefixDir, "/")) {
		http.Error(w, "Invalid subpath", http.StatusBadRequest)
		return
	}

	filesMap := make(map[string][]byte)
	var fileHeaders []*multipart.FileHeader
	if fhs, ok := r.MultipartForm.File["files"]; ok {
		fileHeaders = append(fileHeaders, fhs...)
	}
	if fhs, ok := r.MultipartForm.File["file"]; ok {
		fileHeaders = append(fileHeaders, fhs...)
	}

	if len(fileHeaders) == 0 {
		http.Error(w, "No files uploaded", http.StatusBadRequest)
		return
	}

	var paths []string
	if p, ok := r.MultipartForm.Value["paths"]; ok {
		paths = p
	}

	for i, fh := range fileHeaders {
		var fname string
		if i < len(paths) && strings.TrimSpace(paths[i]) != "" {
			fname = strings.TrimSpace(paths[i])
		} else {
			fname = strings.TrimSpace(fh.Filename)
		}
		fname = strings.ReplaceAll(fname, "\\", "/")
		if fname == "" || strings.Contains(fname, "..") || strings.HasPrefix(fname, "/") {
			continue
		}
		destName := fname
		if prefixDir != "" {
			destName = path.Join(prefixDir, fname)
		}
		f, err := fh.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			continue
		}
		filesMap[destName] = data
	}

	if len(filesMap) == 0 {
		http.Error(w, "No valid files found in upload", http.StatusBadRequest)
		return
	}

	if err := s.cp.UploadDeploymentFiles(r.Context(), id, filesMap); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":        true,
		"files_uploaded": len(filesMap),
	})
}




func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/nodes" && r.URL.Path != "/stacks" && r.URL.Path != "/deployments" {
		http.NotFound(w, r)
		return
	}

	nodes, err := s.store.ListNodes()
	if err != nil {
		http.Error(w, "Failed to list nodes", http.StatusInternalServerError)
		return
	}

	stacks, err := s.store.ListStacks()
	if err != nil {
		http.Error(w, "Failed to list stacks", http.StatusInternalServerError)
		return
	}

	deployments, err := s.store.ListDeployments()
	if err != nil {
		http.Error(w, "Failed to list deployments", http.StatusInternalServerError)
		return
	}

	gitMirrorURL, _ := s.store.GetSetting("git_mirror_url")
	gitMirrorBranch, _ := s.store.GetSetting("git_mirror_branch")
	if gitMirrorBranch == "" {
		gitMirrorBranch = "main"
	}
	gitMirrorStatus, _ := s.store.GetSetting("git_mirror_status")
	if gitMirrorStatus == "" {
		gitMirrorStatus = "idle"
	}
	gitMirrorLastSync, _ := s.store.GetSetting("git_mirror_last_sync")
	gitMirrorError, _ := s.store.GetSetting("git_mirror_error")
	runbookNotes, _ := s.store.GetSetting("global_runbook_notes")
	notifEnabled, _ := s.store.GetSetting("notifications_enabled")
	telegramToken, _ := s.store.GetSetting("telegram_bot_token")
	telegramChatID, _ := s.store.GetSetting("telegram_chat_id")
	webhookURL, _ := s.store.GetSetting("webhook_url")
	pollerEnabled, _ := s.store.GetSetting("poller_enabled")
	pollerInterval, _ := s.store.GetSetting("poller_interval")
	if pollerInterval == "" {
		pollerInterval = "120"
	}

	deploymentsByNode := make(map[string][]store.Deployment)
	for _, d := range deployments {
		deploymentsByNode[d.NodeID] = append(deploymentsByNode[d.NodeID], d)
	}

	data := map[string]interface{}{
		"PublicKeySSH":         s.pubKey,
		"Nodes":                nodes,
		"NodeCount":            len(nodes),
		"Stacks":               stacks,
		"StackCount":           len(stacks),
		"Deployments":          deployments,
		"DeploymentsByNode":    deploymentsByNode,
		"DeploymentCount":      len(deployments),
		"GitMirrorURL":         gitMirrorURL,
		"GitMirrorBranch":      gitMirrorBranch,
		"GitMirrorStatus":      gitMirrorStatus,
		"GitMirrorLastSync":    gitMirrorLastSync,
		"GitMirrorError":       gitMirrorError,
		"RunbookNotes":         runbookNotes,
		"NotificationsEnabled": notifEnabled == "true",
		"TelegramBotToken":     telegramToken,
		"TelegramChatID":       telegramChatID,
		"WebhookURL":           webhookURL,
		"PollerEnabled":        pollerEnabled != "false",
		"PollerInterval":       pollerInterval,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "dashboard.html", data)
}

func (s *Server) handleUpdateRunbook(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	notes := strings.TrimSpace(r.FormValue("runbook_notes"))
	_ = s.store.SetSetting("global_runbook_notes", notes)
	_ = s.triggerGitSync(r.Context())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) buildGitMirrorFiles() (map[string][]byte, error) {
	return s.cp.BuildGitMirrorFiles()
}

func (s *Server) triggerGitSync(ctx context.Context) error {
	return s.cp.SyncGitMirror(ctx)
}

func (s *Server) handleUpdateGitSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	repoURL := strings.TrimSpace(r.FormValue("git_mirror_url"))
	branch := strings.TrimSpace(r.FormValue("git_mirror_branch"))
	if branch == "" {
		branch = "main"
	}

	if repoURL != "" {
		if strings.HasPrefix(repoURL, "-") {
			http.Error(w, "Invalid git mirror repository URL", http.StatusBadRequest)
			return
		}
		lowerURL := strings.ToLower(repoURL)
		if !strings.HasPrefix(lowerURL, "https://") && !strings.HasPrefix(lowerURL, "http://") && !strings.HasPrefix(lowerURL, "ssh://") && !strings.HasPrefix(lowerURL, "git@") {
			http.Error(w, "Invalid git mirror URL protocol: must start with https://, http://, ssh://, or git@", http.StatusBadRequest)
			return
		}
	}

	if strings.HasPrefix(branch, "-") || !gitBranchRegex.MatchString(branch) {
		http.Error(w, "Invalid git mirror branch name", http.StatusBadRequest)
		return
	}

	_ = s.store.SetSetting("git_mirror_url", repoURL)
	_ = s.store.SetSetting("git_mirror_branch", branch)

	_ = s.triggerGitSync(r.Context())

	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		status, _ := s.store.GetSetting("git_mirror_status")
		lastErr, _ := s.store.GetSetting("git_mirror_error")
		lastSync, _ := s.store.GetSetting("git_mirror_last_sync")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   lastErr == "",
			"status":    status,
			"error":     lastErr,
			"last_sync": lastSync,
		})
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSyncGitNow(w http.ResponseWriter, r *http.Request) {
	_ = s.triggerGitSync(r.Context())

	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		status, _ := s.store.GetSetting("git_mirror_status")
		lastErr, _ := s.store.GetSetting("git_mirror_error")
		lastSync, _ := s.store.GetSetting("git_mirror_last_sync")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"success":   lastErr == "",
			"status":    status,
			"error":     lastErr,
			"last_sync": lastSync,
		})
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}


func generateToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type DiskMetrics struct {
	Total   string `json:"total"`
	Used    string `json:"used"`
	Free    string `json:"free"`
	Percent string `json:"percent"`
}

type ContainerInfo struct {
	ID        string `json:"id"`
	Names     string `json:"names"`
	Image     string `json:"image"`
	Status    string `json:"status"`
	State     string `json:"state"`
	Ports     string `json:"ports"`
	Created   string `json:"created"`
	IsManaged bool   `json:"is_managed"`
	StackName string `json:"stack_name,omitempty"`
}

type NodeTelemetry struct {
	Disk       DiskMetrics     `json:"disk"`
	Containers []ContainerInfo `json:"containers"`
	Error      string          `json:"error,omitempty"`
}

func parseDfOutput(out string) DiskMetrics {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return DiskMetrics{}
	}
	fields := strings.Fields(lines[1])
	if len(fields) >= 5 {
		return DiskMetrics{
			Total:   fields[1],
			Used:    fields[2],
			Free:    fields[3],
			Percent: fields[4],
		}
	}
	return DiskMetrics{}
}

type rawDockerPsItem struct {
	ID           string `json:"ID"`
	IDLower      string `json:"id"`
	Names        string `json:"Names"`
	NamesLower   string `json:"names"`
	Image        string `json:"Image"`
	ImageLower   string `json:"image"`
	Status       string `json:"Status"`
	StatusLower  string `json:"status"`
	State        string `json:"State"`
	StateLower   string `json:"state"`
	Ports        string `json:"Ports"`
	PortsLower   string `json:"ports"`
	Created      string `json:"CreatedAt"`
	CreatedLower string `json:"created"`
	Labels       string `json:"Labels"`
	LabelsLower  string `json:"labels"`
}

func (r rawDockerPsItem) getID() string {
	if r.ID != "" {
		return r.ID
	}
	return r.IDLower
}
func (r rawDockerPsItem) getNames() string {
	if r.Names != "" {
		return r.Names
	}
	return r.NamesLower
}
func (r rawDockerPsItem) getImage() string {
	if r.Image != "" {
		return r.Image
	}
	return r.ImageLower
}
func (r rawDockerPsItem) getStatus() string {
	if r.Status != "" {
		return r.Status
	}
	return r.StatusLower
}
func (r rawDockerPsItem) getState() string {
	if r.State != "" {
		return r.State
	}
	if r.StateLower != "" {
		return r.StateLower
	}
	if strings.HasPrefix(strings.ToLower(r.getStatus()), "up") {
		return "running"
	}
	return ""
}
func (r rawDockerPsItem) getPorts() string {
	if r.Ports != "" {
		return r.Ports
	}
	return r.PortsLower
}
func (r rawDockerPsItem) getCreated() string {
	if r.Created != "" {
		return r.Created
	}
	return r.CreatedLower
}
func (r rawDockerPsItem) getLabels() string {
	if r.Labels != "" {
		return r.Labels
	}
	return r.LabelsLower
}

func parseDockerLabels(labelsStr string) map[string]string {
	res := make(map[string]string)
	if labelsStr == "" {
		return res
	}
	parts := strings.Split(labelsStr, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			res[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return res
}

func parseDockerPsOutput(out string, knownStacks map[string]bool) []ContainerInfo {
	var list []ContainerInfo
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw rawDockerPsItem
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		labels := parseDockerLabels(raw.getLabels())
		workingDir := labels["com.docker.compose.project.working_dir"]
		project := labels["com.docker.compose.project"]
		explicitManaged := labels["io.docker-orkes.managed"] == "true"

		isManaged := false
		stackName := ""

		if explicitManaged {
			isManaged = true
			stackName = project
		} else if strings.Contains(workingDir, ".docker-orkes/stacks") {
			isManaged = true
			if project != "" {
				stackName = project
			} else {
				stackName = path.Base(workingDir)
			}
		} else if project != "" && knownStacks != nil && knownStacks[project] {
			isManaged = true
			stackName = project
		}

		list = append(list, ContainerInfo{
			ID:        raw.getID(),
			Names:     raw.getNames(),
			Image:     raw.getImage(),
			Status:    raw.getStatus(),
			State:     raw.getState(),
			Ports:     raw.getPorts(),
			Created:   raw.getCreated(),
			IsManaged: isManaged,
			StackName: stackName,
		})
	}
	return list
}

func (s *Server) handleNodeDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	data := map[string]interface{}{
		"Node": node,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "node_detail.html", data)
}

func (s *Server) handleNodeTelemetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	dfOut, _ := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "df -h /")
	psOut, _ := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker ps -a --format '{{json .}}'")

	deployments, _ := s.store.ListDeploymentsByNode(node.ID)
	knownStacks := make(map[string]bool, len(deployments))
	for _, d := range deployments {
		if d.StackName != "" {
			knownStacks[d.StackName] = true
		}
	}

	telemetry := NodeTelemetry{
		Disk:       parseDfOutput(dfOut),
		Containers: parseDockerPsOutput(psOut, knownStacks),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(telemetry)
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	containerID := strings.TrimSpace(r.PathValue("container_id"))
	if containerID == "" {
		http.Error(w, "Invalid container ID", http.StatusBadRequest)
		return
	}
	for _, c := range containerID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			http.Error(w, "Invalid container ID", http.StatusBadRequest)
			return
		}
	}
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	cmd := fmt.Sprintf("docker logs --tail 100 %s", containerID)
	logs, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, cmd)
	if err != nil {
		http.Error(w, "Failed to get logs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(logs))
}

type rawDockerImage struct {
	ID           string `json:"ID"`
	IDLower      string `json:"id"`
	Repository   string `json:"Repository"`
	RepoLower    string `json:"repository"`
	Tag          string `json:"Tag"`
	TagLower     string `json:"tag"`
	Size         string `json:"Size"`
	SizeLower    string `json:"size"`
	CreatedSince string `json:"CreatedSince"`
	SinceLower   string `json:"created_since"`
}

func (r rawDockerImage) getID() string {
	if r.ID != "" {
		return r.ID
	}
	return r.IDLower
}
func (r rawDockerImage) getRepo() string {
	if r.Repository != "" {
		return r.Repository
	}
	return r.RepoLower
}
func (r rawDockerImage) getTag() string {
	if r.Tag != "" {
		return r.Tag
	}
	return r.TagLower
}
func (r rawDockerImage) getSize() string {
	if r.Size != "" {
		return r.Size
	}
	return r.SizeLower
}
func (r rawDockerImage) getSince() string {
	if r.CreatedSince != "" {
		return r.CreatedSince
	}
	return r.SinceLower
}

type NodeImageInfo struct {
	ID           string   `json:"id"`
	Repository   string   `json:"repository"`
	Tag          string   `json:"tag"`
	Size         string   `json:"size"`
	CreatedSince string   `json:"created_since"`
	Status       string   `json:"status"` // in_use_running, in_use_stopped, on_demand, dangling, unused
	IsPinned     bool     `json:"is_pinned"`
	Containers   []string `json:"containers"`
	IsManaged    bool     `json:"is_managed"`
	UsageOrigin  string   `json:"usage_origin,omitempty"` // "managed", "unmanaged", or ""
}

func parseDockerImagesOutput(out string, containers []ContainerInfo, pinnedMap map[string]bool) []NodeImageInfo {
	var list []NodeImageInfo
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw rawDockerImage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		repo := raw.getRepo()
		tag := raw.getTag()
		id := raw.getID()
		size := raw.getSize()
		since := raw.getSince()

		isDangling := (repo == "<none>" && tag == "<none>") || (repo == "<none>" && tag == "") || tag == "<none>" || tag == ""
		imageRef := repo
		if isDangling {
			if repo == "<none>" || repo == "" {
				imageRef = "<none>:<none>"
			} else {
				imageRef = fmt.Sprintf("%s:<none>", repo)
			}
		} else if tag != "" && tag != "<none>" {
			imageRef = fmt.Sprintf("%s:%s", repo, tag)
		}

		isPinned := pinnedMap[imageRef] || pinnedMap[repo]

		hasRunning := false
		hasStopped := false
		hasManaged := false
		hasUnmanaged := false
		var matchedContainers []string

		for _, c := range containers {
			cImg := strings.TrimSpace(c.Image)
			cImgNoSha := strings.TrimPrefix(cImg, "sha256:")
			idNoSha := strings.TrimPrefix(id, "sha256:")

			match := false
			if !isDangling && cImg != "" && (cImg == imageRef || cImg == repo) {
				match = true
			} else if !isDangling && (tag == "latest" || tag == "") && cImg == repo+":latest" {
				match = true
			} else if idNoSha != "" && cImgNoSha != "" {
				if idNoSha == cImgNoSha || strings.HasPrefix(idNoSha, cImgNoSha) || strings.HasPrefix(cImgNoSha, idNoSha) {
					match = true
				}
			}

			if match {
				cName := strings.TrimPrefix(c.Names, "/")
				matchedContainers = append(matchedContainers, cName)
				if strings.ToLower(c.State) == "running" {
					hasRunning = true
				} else {
					hasStopped = true
				}
				if c.IsManaged {
					hasManaged = true
				} else {
					hasUnmanaged = true
				}
			}
		}

		status := "unused"
		if hasRunning {
			status = "in_use_running"
		} else if hasStopped {
			status = "in_use_stopped"
		} else if isDangling {
			status = "dangling"
		} else if isPinned {
			status = "on_demand"
		}

		usageOrigin := ""
		if hasManaged {
			usageOrigin = "managed"
		} else if hasUnmanaged {
			usageOrigin = "unmanaged"
		}

		list = append(list, NodeImageInfo{
			ID:           id,
			Repository:   repo,
			Tag:          tag,
			Size:         size,
			CreatedSince: since,
			Status:       status,
			IsPinned:     isPinned,
			Containers:   matchedContainers,
			IsManaged:    hasManaged,
			UsageOrigin:  usageOrigin,
		})
	}
	return list
}

func (s *Server) handleListNodeImages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	pinnedList, _ := s.store.ListNodePinnedImages(node.ID)
	pinnedMap := make(map[string]bool, len(pinnedList))
	for _, ref := range pinnedList {
		pinnedMap[ref] = true
	}

	imgOut, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker images -a --format '{{json .}}'")
	if err != nil {
		http.Error(w, "Failed to query node images: "+err.Error(), http.StatusInternalServerError)
		return
	}

	psOut, _ := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker ps -a --format '{{json .}}'")

	deployments, _ := s.store.ListDeploymentsByNode(node.ID)
	knownStacks := make(map[string]bool, len(deployments))
	for _, d := range deployments {
		if d.StackName != "" {
			knownStacks[d.StackName] = true
		}
	}

	containers := parseDockerPsOutput(psOut, knownStacks)

	images := parseDockerImagesOutput(imgOut, containers, pinnedMap)
	if images == nil {
		images = []NodeImageInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(images)
}

func (s *Server) handlePinNodeImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	var req struct {
		ImageRef string `json:"image_ref"`
		Pinned   *bool  `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	imageRef := strings.TrimSpace(req.ImageRef)
	if imageRef == "" || imageRef == "<none>:<none>" {
		http.Error(w, "Valid image reference required for pinning", http.StatusBadRequest)
		return
	}

	pinnedList, _ := s.store.ListNodePinnedImages(node.ID)
	isCurrentlyPinned := false
	for _, p := range pinnedList {
		if p == imageRef {
			isCurrentlyPinned = true
			break
		}
	}

	targetPinned := !isCurrentlyPinned
	if req.Pinned != nil {
		targetPinned = *req.Pinned
	}

	if targetPinned {
		if err := s.store.PinNodeImage(node.ID, imageRef); err != nil {
			http.Error(w, "Failed to pin image: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := s.store.UnpinNodeImage(node.ID, imageRef); err != nil {
			http.Error(w, "Failed to unpin image: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":   true,
		"is_pinned": targetPinned,
		"image_ref": imageRef,
	})
}

func (s *Server) handlePullNodeImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	var req struct {
		ImageRef string `json:"image_ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	imageRef := strings.TrimSpace(req.ImageRef)
	if imageRef == "" || imageRef == "<none>:<none>" {
		http.Error(w, "Valid image reference required for pulling", http.StatusBadRequest)
		return
	}

	for _, c := range imageRef {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == ':' || c == '/' || c == '.' || c == '_' || c == '-' || c == '@') {
			http.Error(w, "Invalid characters in image target", http.StatusBadRequest)
			return
		}
	}

	pullCtx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	cmd := fmt.Sprintf("docker pull %s", imageRef)
	out, err := s.executor.RunCommand(pullCtx, node.Host, node.Port, node.User, s.privKey, cmd)
	if err != nil {
		http.Error(w, "Failed to pull image: "+err.Error()+"\nOutput: "+out, http.StatusInternalServerError)
		return
	}

	status := "pulled"
	outLower := strings.ToLower(out)
	if strings.Contains(outLower, "image is up to date") {
		status = "up_to_date"
	} else if strings.Contains(outLower, "downloaded newer image") || strings.Contains(outLower, "pulling fs layer") {
		status = "updated"
	}

	deployments, _ := s.store.ListDeploymentsByNode(node.ID)
	var usedByDeps []map[string]string

	psOut, _ := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker ps -a --format '{{json .}}'")
	knownStacks := make(map[string]bool, len(deployments))
	depByStack := make(map[string]store.Deployment, len(deployments))
	for _, d := range deployments {
		if d.StackName != "" {
			knownStacks[d.StackName] = true
			depByStack[d.StackName] = d
		}
	}
	containers := parseDockerPsOutput(psOut, knownStacks)

	seenDeps := make(map[string]bool)
	targetNoSha := strings.TrimPrefix(imageRef, "sha256:")
	for _, c := range containers {
		if !c.IsManaged || c.StackName == "" {
			continue
		}
		cImg := strings.TrimSpace(c.Image)
		cImgNoSha := strings.TrimPrefix(cImg, "sha256:")

		match := false
		if cImg == imageRef || cImgNoSha == targetNoSha || strings.HasPrefix(imageRef, cImg+":") || strings.HasPrefix(cImg, imageRef+":") {
			match = true
		} else if targetNoSha != "" && (cImgNoSha == targetNoSha || strings.HasPrefix(cImgNoSha, targetNoSha)) {
			match = true
		}

		if match {
			if dep, exists := depByStack[c.StackName]; exists && !seenDeps[dep.ID] {
				seenDeps[dep.ID] = true
				usedByDeps = append(usedByDeps, map[string]string{
					"id":         dep.ID,
					"stack_name": dep.StackName,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success":             true,
		"image_ref":           imageRef,
		"status":              status,
		"output":              out,
		"used_by_deployments": usedByDeps,
	})
}

func (s *Server) handleDeleteNodeImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	var req struct {
		ImageRef string `json:"image_ref"`
		ImageID  string `json:"image_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	target := strings.TrimSpace(req.ImageRef)
	if target == "" || target == "<none>:<none>" || strings.HasSuffix(target, ":<none>") {
		target = strings.TrimSpace(req.ImageID)
	}
	if target == "" {
		http.Error(w, "image_ref or image_id required", http.StatusBadRequest)
		return
	}

	// Validate target against command injection
	for _, c := range target {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == ':' || c == '/' || c == '.' || c == '_' || c == '-' || c == '@') {
			http.Error(w, "Invalid characters in image target", http.StatusBadRequest)
			return
		}
	}

	// Hard Block: check if image is pinned as On-Demand Image
	pinnedList, _ := s.store.ListNodePinnedImages(node.ID)
	for _, p := range pinnedList {
		if p == target || (req.ImageRef != "" && p == req.ImageRef) {
			http.Error(w, "Cannot delete image: pinned as an On-Demand Image. Unpin it first before deleting.", http.StatusBadRequest)
			return
		}
	}

	// Hard Block: check if image is in use by running or stopped containers
	psOut, _ := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker ps -a --format '{{json .}}'")
	containers := parseDockerPsOutput(psOut, nil)

	targetNoSha := strings.TrimPrefix(target, "sha256:")
	idNoSha := strings.TrimPrefix(req.ImageID, "sha256:")

	var usedBy []string
	for _, c := range containers {
		cImg := strings.TrimSpace(c.Image)
		cImgNoSha := strings.TrimPrefix(cImg, "sha256:")

		match := false
		if target != "" && target != "<none>:<none>" && (cImg == target || (req.ImageRef != "" && cImg == req.ImageRef)) {
			match = true
		} else if idNoSha != "" && cImgNoSha != "" && (cImgNoSha == idNoSha || strings.HasPrefix(idNoSha, cImgNoSha) || strings.HasPrefix(cImgNoSha, idNoSha)) {
			match = true
		} else if targetNoSha != "" && cImgNoSha != "" && (cImgNoSha == targetNoSha || strings.HasPrefix(targetNoSha, cImgNoSha) || strings.HasPrefix(cImgNoSha, targetNoSha)) {
			match = true
		}

		if match {
			cName := strings.TrimPrefix(c.Names, "/")
			usedBy = append(usedBy, fmt.Sprintf("%s (%s)", cName, c.State))
		}
	}

	if len(usedBy) > 0 {
		http.Error(w, fmt.Sprintf("Cannot delete image: in use by container(s): %s", strings.Join(usedBy, ", ")), http.StatusBadRequest)
		return
	}

	cmd := fmt.Sprintf("docker rmi %s", target)
	out, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, cmd)
	if err != nil {
		http.Error(w, "Failed to delete image: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  out,
	})
}


func (s *Server) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	revs, err := s.cp.ListRevisions(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(revs)
}

func (s *Server) handleRollbackDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	revisionID := strings.TrimSpace(r.FormValue("revision_id"))
	if revisionID == "" {
		http.Error(w, "revision_id is required", http.StatusBadRequest)
		return
	}

	if err := s.cp.Rollback(r.Context(), id, revisionID); err != nil {
		http.Error(w, "Rollback failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleUpdateNotificationSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	telegramToken := strings.TrimSpace(r.FormValue("telegram_bot_token"))
	telegramChatID := strings.TrimSpace(r.FormValue("telegram_chat_id"))
	webhookURL := strings.TrimSpace(r.FormValue("webhook_url"))
	enabled := r.FormValue("notifications_enabled") == "on" || r.FormValue("notifications_enabled") == "true"

	_ = s.store.SetSetting("telegram_bot_token", telegramToken)
	_ = s.store.SetSetting("telegram_chat_id", telegramChatID)
	_ = s.store.SetSetting("webhook_url", webhookURL)
	enabledStr := "false"
	if enabled {
		enabledStr = "true"
	}
	_ = s.store.SetSetting("notifications_enabled", enabledStr)

	s.mu.Lock()
	s.notifier = notifier.New(notifier.Config{
		TelegramBotToken: telegramToken,
		TelegramChatID:   telegramChatID,
		WebhookURL:       webhookURL,
		Enabled:          enabled,
	})
	s.mu.Unlock()

	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	n := s.notifier
	s.mu.RUnlock()

	err := n.SendAlert(r.Context(), "Test Alert", "This is a test notification from Docker Orkes.", false)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

func (s *Server) handleUpdatePollerSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	enabled := r.FormValue("poller_enabled") == "on" || r.FormValue("poller_enabled") == "true"
	interval := strings.TrimSpace(r.FormValue("poller_interval"))
	if interval == "" {
		interval = "120"
	}

	enabledStr := "false"
	if enabled {
		enabledStr = "true"
	}
	_ = s.store.SetSetting("poller_enabled", enabledStr)
	_ = s.store.SetSetting("poller_interval", interval)

	s.mu.Lock()
	if s.poller != nil {
		s.poller.Stop()
	}
	newInterval := poller.ParseIntervalOrDefault(interval, 2*time.Minute)
	s.poller = poller.New(poller.Config{
		Engine:   s.cp,
		Notifier: s.notifier,
		Interval: newInterval,
	})
	if enabled {
		s.poller.Start()
	}
	s.mu.Unlock()

	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type terminalResizeMsg struct {
	Type string `json:"type"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
}

type wsTerminalRW struct {
	r     io.Reader
	w     io.Writer
	touch func()
}

func (rw *wsTerminalRW) Read(p []byte) (int, error) {
	n, err := rw.r.Read(p)
	if n > 0 && rw.touch != nil {
		rw.touch()
	}
	return n, err
}

func (rw *wsTerminalRW) Write(p []byte) (int, error) {
	n, err := rw.w.Write(p)
	if n > 0 && rw.touch != nil {
		rw.touch()
	}
	return n, err
}

func (s *Server) handleTerminalWebSocket(ws *websocket.Conn) {
	defer ws.Close()

	r := ws.Request()
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			_, _ = ws.Write([]byte("\r\n\x1b[31mError: Cross-origin WebSocket connection rejected\x1b[0m\r\n"))
			return
		}
	}

	nodeID := strings.TrimSpace(r.URL.Query().Get("node_id"))
	if nodeID == "" {
		_, _ = ws.Write([]byte("\r\n\x1b[31mError: node_id parameter is required\x1b[0m\r\n"))
		return
	}

	node, err := s.store.GetNode(nodeID)
	if err != nil || node == nil {
		_, _ = ws.Write([]byte("\r\n\x1b[31mError: Node not found\x1b[0m\r\n"))
		return
	}

	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	cmd := ""
	containerID := strings.TrimSpace(r.URL.Query().Get("container_id"))
	if containerID != "" {
		for _, c := range containerID {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
				_, _ = ws.Write([]byte("\r\n\x1b[31mError: Invalid container ID\x1b[0m\r\n"))
				return
			}
		}
		// ponytail: Smart shell fallback: test if /bin/bash exists, else fallback to /bin/sh for Alpine/BusyBox
		cmd = fmt.Sprintf("docker exec -it %s /bin/sh -c 'if [ -x /bin/bash ]; then exec /bin/bash; else exec /bin/sh; fi'", containerID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	idleTimer := time.NewTimer(30 * time.Minute)
	defer idleTimer.Stop()

	touch := func() {
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(30 * time.Minute)
	}

	go func() {
		select {
		case <-idleTimer.C:
			_, _ = ws.Write([]byte("\r\n\x1b[33mSession closed due to 30 minutes of inactivity.\x1b[0m\r\n"))
			cancel()
			_ = ws.Close()
		case <-ctx.Done():
		}
	}()

	resizeChan := make(chan [2]int, 10)
	pr, pw := io.Pipe()

	// Read from WebSocket: separate resize commands from stdin keystrokes
	go func() {
		defer pw.Close()
		buf := make([]byte, 4096)
		for {
			n, err := ws.Read(buf)
			if err != nil {
				cancel()
				return
			}
			touch()
			data := buf[:n]
			trimmed := bytes.TrimSpace(data)
			if bytes.HasPrefix(trimmed, []byte(`{"type":"resize"`)) {
				var msg terminalResizeMsg
				if err := json.Unmarshal(trimmed, &msg); err == nil && msg.Type == "resize" {
					if msg.Rows > 0 && msg.Cols > 0 {
						select {
						case <-ctx.Done():
							return
						case resizeChan <- [2]int{msg.Rows, msg.Cols}:
						default:
						}
					}
					continue
				}
			}
			if _, err := pw.Write(data); err != nil {
				return
			}
		}
	}()

	rw := &wsTerminalRW{
		r:     pr,
		w:     ws,
		touch: touch,
	}

	err = s.executor.RunInteractiveTerminal(ctx, node.Host, node.Port, node.User, s.privKey, cmd, rows, cols, rw, resizeChan)
	if err != nil && ctx.Err() == nil {
		_, _ = ws.Write([]byte(fmt.Sprintf("\r\n\x1b[31mTerminal session ended: %v\x1b[0m\r\n", err)))
	} else if ctx.Err() == nil {
		_, _ = ws.Write([]byte("\r\n\x1b[32mTerminal session closed.\x1b[0m\r\n"))
	}
}

func parseDockerVolumesOutput(volOut string, psInspectOut string) []nodeclient.NodeVolumeInfo {
	var list []nodeclient.NodeVolumeInfo

	type rawVol struct {
		Name       string `json:"Name"`
		Driver     string `json:"Driver"`
		Scope      string `json:"Scope"`
		Size       string `json:"Size"`
		Labels     string `json:"Labels"`
		LabelMap   map[string]string
	}

	// Map volume name -> attached container names
	volContainers := make(map[string][]string)
	if psInspectOut != "" {
		var containersInspect []struct {
			Name   string `json:"Name"`
			Mounts []struct {
				Type string `json:"Type"`
				Name string `json:"Name"`
			} `json:"Mounts"`
		}
		if err := json.Unmarshal([]byte(psInspectOut), &containersInspect); err == nil {
			for _, c := range containersInspect {
				cName := strings.TrimPrefix(c.Name, "/")
				for _, m := range c.Mounts {
					if m.Type == "volume" && m.Name != "" {
						volContainers[m.Name] = append(volContainers[m.Name], cName)
					}
				}
			}
		}
	}

	lines := strings.Split(volOut, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var v rawVol
		if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
			continue
		}
		if v.Name == "" {
			continue
		}

		isManaged := false
		stackName := ""
		// Labels can be a comma-separated list or JSON in docker format
		if strings.Contains(v.Labels, "com.docker.compose.project=") {
			isManaged = true
			parts := strings.Split(v.Labels, ",")
			for _, p := range parts {
				if strings.HasPrefix(strings.TrimSpace(p), "com.docker.compose.project=") {
					stackName = strings.TrimPrefix(strings.TrimSpace(p), "com.docker.compose.project=")
				}
			}
		}

		attached := volContainers[v.Name]
		inUse := len(attached) > 0

		list = append(list, nodeclient.NodeVolumeInfo{
			Name:       v.Name,
			Driver:     v.Driver,
			Scope:      v.Scope,
			Size:       v.Size,
			IsManaged:  isManaged,
			StackName:  stackName,
			InUse:      inUse,
			Containers: attached,
		})
	}
	return list
}

func parseDockerNetworksOutput(netOut string, psInspectOut string) []nodeclient.NodeNetworkInfo {
	var list []nodeclient.NodeNetworkInfo

	type rawNet struct {
		ID     string `json:"ID"`
		Name   string `json:"Name"`
		Driver string `json:"Driver"`
		Scope  string `json:"Scope"`
		Labels string `json:"Labels"`
	}

	// Map network name/ID -> attached container names
	netContainers := make(map[string][]string)
	if psInspectOut != "" {
		var containersInspect []struct {
			Name            string `json:"Name"`
			NetworkSettings struct {
				Networks map[string]struct {
					NetworkID string `json:"NetworkID"`
				} `json:"Networks"`
			} `json:"NetworkSettings"`
		}
		if err := json.Unmarshal([]byte(psInspectOut), &containersInspect); err == nil {
			for _, c := range containersInspect {
				cName := strings.TrimPrefix(c.Name, "/")
				for netName, nInfo := range c.NetworkSettings.Networks {
					if netName != "" {
						netContainers[netName] = append(netContainers[netName], cName)
					}
					if nInfo.NetworkID != "" {
						netContainers[nInfo.NetworkID] = append(netContainers[nInfo.NetworkID], cName)
					}
				}
			}
		}
	}

	lines := strings.Split(netOut, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var n rawNet
		if err := json.Unmarshal([]byte(trimmed), &n); err != nil {
			continue
		}
		if n.Name == "" {
			continue
		}

		isSystem := n.Name == "bridge" || n.Name == "host" || n.Name == "none"
		isManaged := false
		stackName := ""
		if strings.Contains(n.Labels, "com.docker.compose.project=") {
			isManaged = true
			parts := strings.Split(n.Labels, ",")
			for _, p := range parts {
				if strings.HasPrefix(strings.TrimSpace(p), "com.docker.compose.project=") {
					stackName = strings.TrimPrefix(strings.TrimSpace(p), "com.docker.compose.project=")
				}
			}
		}

		attached := netContainers[n.Name]
		if len(attached) == 0 && n.ID != "" {
			attached = netContainers[n.ID]
		}
		inUse := isSystem || len(attached) > 0

		list = append(list, nodeclient.NodeNetworkInfo{
			ID:         n.ID,
			Name:       n.Name,
			Driver:     n.Driver,
			Scope:      n.Scope,
			IsSystem:   isSystem,
			IsManaged:  isManaged,
			StackName:  stackName,
			InUse:      inUse,
			Containers: attached,
		})
	}
	return list
}

func (s *Server) handleListNodeVolumes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	volOut, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker volume ls --format '{{json .}}'")
	if err != nil {
		http.Error(w, "Failed to query volumes: "+err.Error(), http.StatusInternalServerError)
		return
	}

	inspectOut, _ := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker ps -a -q | xargs -r docker inspect --format '{{json .}}' 2>/dev/null || true")
	// If inspect fails or returns multi JSON objects without wrapping array, we parse line-by-line
	if inspectOut != "" && !strings.HasPrefix(strings.TrimSpace(inspectOut), "[") {
		// Wrap JSON lines into JSON array
		var lines []string
		for _, l := range strings.Split(inspectOut, "\n") {
			if tr := strings.TrimSpace(l); tr != "" {
				lines = append(lines, tr)
			}
		}
		inspectOut = "[" + strings.Join(lines, ",") + "]"
	}

	volumes := parseDockerVolumesOutput(volOut, inspectOut)
	if volumes == nil {
		volumes = []nodeclient.NodeVolumeInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(volumes)
}

func (s *Server) handleDeleteNodeVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	volName := strings.TrimSpace(req.Name)
	if volName == "" {
		http.Error(w, "Volume name required", http.StatusBadRequest)
		return
	}

	for _, c := range volName {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			http.Error(w, "Invalid characters in volume name", http.StatusBadRequest)
			return
		}
	}

	cmd := fmt.Sprintf("docker volume rm %s", volName)
	out, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, cmd)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete volume: %v (%s)", err, strings.TrimSpace(out)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  out,
	})
}

func (s *Server) handlePruneNodeVolumes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	out, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker volume prune -f")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to prune volumes: %v (%s)", err, strings.TrimSpace(out)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  out,
	})
}

func (s *Server) handleListNodeNetworks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	netOut, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker network ls --format '{{json .}}'")
	if err != nil {
		http.Error(w, "Failed to query networks: "+err.Error(), http.StatusInternalServerError)
		return
	}

	inspectOut, _ := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker ps -a -q | xargs -r docker inspect --format '{{json .}}' 2>/dev/null || true")
	if inspectOut != "" && !strings.HasPrefix(strings.TrimSpace(inspectOut), "[") {
		var lines []string
		for _, l := range strings.Split(inspectOut, "\n") {
			if tr := strings.TrimSpace(l); tr != "" {
				lines = append(lines, tr)
			}
		}
		inspectOut = "[" + strings.Join(lines, ",") + "]"
	}

	networks := parseDockerNetworksOutput(netOut, inspectOut)
	if networks == nil {
		networks = []nodeclient.NodeNetworkInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(networks)
}

func (s *Server) handleDeleteNodeNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	target := strings.TrimSpace(req.ID)
	if target == "" {
		target = strings.TrimSpace(req.Name)
	}
	if target == "" {
		http.Error(w, "Network ID or name required", http.StatusBadRequest)
		return
	}

	// System networks protection
	if req.Name == "bridge" || req.Name == "host" || req.Name == "none" || target == "bridge" || target == "host" || target == "none" {
		http.Error(w, "Cannot delete default system network", http.StatusBadRequest)
		return
	}

	for _, c := range target {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			http.Error(w, "Invalid characters in network target", http.StatusBadRequest)
			return
		}
	}

	cmd := fmt.Sprintf("docker network rm %s", target)
	out, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, cmd)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete network: %v (%s)", err, strings.TrimSpace(out)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  out,
	})
}

func (s *Server) handlePruneNodeNetworks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	out, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, "docker network prune -f")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to prune networks: %v (%s)", err, strings.TrimSpace(out)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"output":  out,
	})
}




