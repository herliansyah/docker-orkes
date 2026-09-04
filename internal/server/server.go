package server

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"sync"

	"docker-orkes/internal/controlplane"
	"docker-orkes/internal/drift"
	"docker-orkes/internal/gitsync"
	"docker-orkes/internal/notifier"
	"docker-orkes/internal/poller"
	"docker-orkes/internal/ssh"
	"docker-orkes/internal/store"

	"gopkg.in/yaml.v3"
)

//go:embed templates/*.html
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
	mux.Handle("GET /nodes/{id}/containers/{container_id}/logs", s.requireAuth(http.HandlerFunc(s.handleContainerLogs)))

	mux.Handle("GET /stacks", s.requireAuth(http.HandlerFunc(s.handleListStacks)))
	mux.Handle("POST /stacks", s.requireAuth(http.HandlerFunc(s.handleCreateStack)))
	mux.Handle("POST /stacks/{id}/update", s.requireAuth(http.HandlerFunc(s.handleUpdateStack)))
	mux.Handle("POST /stacks/{id}/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteStack)))

	mux.Handle("GET /deployments", s.requireAuth(http.HandlerFunc(s.handleListDeployments)))
	mux.Handle("POST /deployments", s.requireAuth(http.HandlerFunc(s.handleCreateDeployment)))
	mux.Handle("POST /deployments/{id}/update", s.requireAuth(http.HandlerFunc(s.handleUpdateDeployment)))
	mux.Handle("POST /deployments/{id}/up", s.requireAuth(http.HandlerFunc(s.handleUpDeployment)))
	mux.Handle("POST /deployments/{id}/restart", s.requireAuth(http.HandlerFunc(s.handleRestartDeployment)))
	mux.Handle("POST /deployments/{id}/down", s.requireAuth(http.HandlerFunc(s.handleDownDeployment)))
	mux.Handle("POST /deployments/{id}/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteDeployment)))


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
	_, _ = s.cp.TestNode(r.Context(), id)
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



func (s *Server) handleUpDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cp.Deploy(r.Context(), id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleRestartDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cp.Restart(r.Context(), id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDownDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = s.cp.Down(r.Context(), id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteDeployment(id); err != nil {
		http.Error(w, "Failed to delete deployment: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.cp.SyncGitMirror(r.Context())
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
	_, _ = s.cp.CheckDrift(r.Context(), id)
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

	for _, fh := range fileHeaders {
		fname := strings.TrimSpace(fh.Filename)
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
	ID      string `json:"id"`
	Names   string `json:"names"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	State   string `json:"state"`
	Ports   string `json:"ports"`
	Created string `json:"created"`
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

func parseDockerPsOutput(out string) []ContainerInfo {
	var list []ContainerInfo
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var c ContainerInfo
		if err := json.Unmarshal([]byte(line), &c); err == nil {
			list = append(list, c)
		}
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

	telemetry := NodeTelemetry{
		Disk:       parseDfOutput(dfOut),
		Containers: parseDockerPsOutput(psOut),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(telemetry)
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	containerID := r.PathValue("container_id")

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




