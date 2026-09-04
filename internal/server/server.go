package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"


	"docker-orkes/internal/diff"
	"docker-orkes/internal/gitsync"
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
	executor     ssh.Executor
	gitSyncer    gitsync.Syncer
	gitMirrorDir string
	templates    *template.Template
	pubKey       string
	privKey      string
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

	return &Server{
		cfg:          cfg,
		store:        st,
		executor:     cfg.Executor,
		gitSyncer:    cfg.GitSyncer,
		gitMirrorDir: cfg.GitMirrorDir,
		templates:    tmpl,
		pubKey:       pubKey,
		privKey:      privKey,
	}, nil

}

func (s *Server) Store() *store.Store {
	return s.store
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


	mux.Handle("POST /settings/git", s.requireAuth(http.HandlerFunc(s.handleUpdateGitSettings)))
	mux.Handle("POST /settings/git/sync", s.requireAuth(http.HandlerFunc(s.handleSyncGitNow)))
	mux.Handle("POST /settings/runbook", s.requireAuth(http.HandlerFunc(s.handleUpdateRunbook)))

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
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	sshPassword := strings.TrimSpace(r.FormValue("ssh_password"))
	if sshPassword == "" {
		http.Error(w, "SSH Password is required", http.StatusBadRequest)
		return
	}

	if err := s.executor.BootstrapKey(r.Context(), node.Host, node.Port, node.User, sshPassword, s.pubKey); err != nil {
		_ = s.store.UpdateNodeStatus(node.ID, "offline", "Auto-inject key failed: "+err.Error(), "")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	dockerVer, err := s.executor.TestConnection(r.Context(), node.Host, node.Port, node.User, s.privKey)
	if err != nil {
		_ = s.store.UpdateNodeStatus(node.ID, "offline", err.Error(), "")
	} else {
		_ = s.store.UpdateNodeStatus(node.ID, "online", "", dockerVer)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}


func (s *Server) handleTestNode(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	node, err := s.store.GetNode(id)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	dockerVer, err := s.executor.TestConnection(r.Context(), node.Host, node.Port, node.User, s.privKey)
	if err != nil {
		_ = s.store.UpdateNodeStatus(node.ID, "offline", err.Error(), "")
	} else {
		_ = s.store.UpdateNodeStatus(node.ID, "online", "", dockerVer)
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
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}

	_ = s.deployStack(r.Context(), dep)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleRestartDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}
	stack, err := s.store.GetStack(dep.StackID)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	node, err := s.store.GetNode(dep.NodeID)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	remoteDir := fmt.Sprintf(".docker-orkes/stacks/%s", stack.Name)
	cmd := fmt.Sprintf("docker compose -f %s/docker-compose.yml restart", remoteDir)
	out, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, cmd)
	if err != nil {
		_ = s.store.UpdateDeploymentStatus(dep.ID, "failed", err.Error())
	} else {
		_ = s.store.UpdateDeploymentStatus(dep.ID, "running", out)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDownDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}
	stack, err := s.store.GetStack(dep.StackID)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	node, err := s.store.GetNode(dep.NodeID)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	remoteDir := fmt.Sprintf(".docker-orkes/stacks/%s", stack.Name)
	cmd := fmt.Sprintf("docker compose -f %s/docker-compose.yml down", remoteDir)
	out, err := s.executor.RunCommand(r.Context(), node.Host, node.Port, node.User, s.privKey, cmd)
	if err != nil {
		_ = s.store.UpdateDeploymentStatus(dep.ID, "failed", err.Error())
	} else {
		_ = s.store.UpdateDeploymentStatus(dep.ID, "stopped", out)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteDeployment(id); err != nil {
		http.Error(w, "Failed to delete deployment: "+err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) deployStack(ctx context.Context, dep *store.Deployment) error {
	// Allow up to 10 minutes for image download and container startup
	deployCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	stack, err := s.store.GetStack(dep.StackID)
	if err != nil {
		return fmt.Errorf("stack not found: %w", err)
	}
	node, err := s.store.GetNode(dep.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	remoteDir := fmt.Sprintf(".docker-orkes/stacks/%s", stack.Name)
	files := map[string][]byte{
		"docker-compose.yml": []byte(stack.ComposeYAML),
		".env":               []byte(dep.EnvVars),
	}

	if err := s.executor.UploadFiles(deployCtx, node.Host, node.Port, node.User, s.privKey, remoteDir, files); err != nil {
		_ = s.store.UpdateDeploymentStatus(dep.ID, "failed", err.Error())
		return fmt.Errorf("failed to upload files: %w", err)
	}

	cmd := fmt.Sprintf("docker compose -f %s/docker-compose.yml --env-file %s/.env up -d", remoteDir, remoteDir)
	out, err := s.executor.RunCommand(deployCtx, node.Host, node.Port, node.User, s.privKey, cmd)
	if err != nil {
		_ = s.store.UpdateDeploymentStatus(dep.ID, "failed", fmt.Sprintf("command error: %v (out: %s)", err, out))
		return fmt.Errorf("failed to run docker compose up: %w", err)
	}


	hash := computeDesiredHash(stack.ComposeYAML, dep.EnvVars)
	_ = s.store.UpdateDeploymentDesiredHash(dep.ID, hash)
	_ = s.store.UpdateDeploymentStatus(dep.ID, "running", out)
	return nil
}

func computeDesiredHash(composeYAML, envVars string) string {
	h := sha256.New()
	h.Write([]byte(composeYAML))
	h.Write([]byte("\n---\n"))
	h.Write([]byte(envVars))
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) handleCheckDrift(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}
	stack, err := s.store.GetStack(dep.StackID)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	node, err := s.store.GetNode(dep.NodeID)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	remoteDir := fmt.Sprintf(".docker-orkes/stacks/%s", stack.Name)
	composeBytes, err1 := s.executor.ReadRemoteFile(r.Context(), node.Host, node.Port, node.User, s.privKey, remoteDir+"/docker-compose.yml")
	envBytes, _ := s.executor.ReadRemoteFile(r.Context(), node.Host, node.Port, node.User, s.privKey, remoteDir+"/.env")

	if err1 != nil {
		_ = s.store.UpdateDeploymentDrift(dep.ID, "drifted", "Remote compose file missing: "+err1.Error(), "", "")
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	remoteCompose := string(composeBytes)
	remoteEnv := string(envBytes)
	actualHash := computeDesiredHash(remoteCompose, remoteEnv)

	if actualHash == dep.DesiredHash {
		_ = s.store.UpdateDeploymentDrift(dep.ID, "synced", "", remoteCompose, remoteEnv)
	} else {
		diffOutput := diff.UnifiedDiff(stack.Name+" (Desired)", stack.ComposeYAML+"\n# .env\n"+dep.EnvVars, stack.Name+" (Actual)", remoteCompose+"\n# .env\n"+remoteEnv)
		_ = s.store.UpdateDeploymentDrift(dep.ID, "drifted", diffOutput, remoteCompose, remoteEnv)
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleReconcileDrift(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}
	_ = s.deployStack(r.Context(), dep)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleAdoptDrift(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}
	stack, err := s.store.GetStack(dep.StackID)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}

	if dep.RemoteCompose != "" {
		stack.ComposeYAML = dep.RemoteCompose
		_ = s.store.UpdateStack(*stack)
	}

	actualHash := computeDesiredHash(stack.ComposeYAML, dep.RemoteEnv)
	_ = s.store.UpdateDeploymentEnvAndHash(dep.ID, dep.RemoteEnv, actualHash)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleListDeploymentFiles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}
	stack, err := s.store.GetStack(dep.StackID)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	node, err := s.store.GetNode(dep.NodeID)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	remoteDir := fmt.Sprintf(".docker-orkes/stacks/%s", stack.Name)
	files, err := s.executor.ListRemoteFiles(r.Context(), node.Host, node.Port, node.User, s.privKey, remoteDir)
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
		"files":      files,
	})
}

func (s *Server) handleViewDeploymentFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	fileName := strings.TrimSpace(r.URL.Query().Get("file"))
	if fileName == "" || strings.Contains(fileName, "..") {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	dep, err := s.store.GetDeployment(id)
	if err != nil {
		http.Error(w, "Deployment not found", http.StatusNotFound)
		return
	}
	stack, err := s.store.GetStack(dep.StackID)
	if err != nil {
		http.Error(w, "Stack not found", http.StatusNotFound)
		return
	}
	node, err := s.store.GetNode(dep.NodeID)
	if err != nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	remoteFilePath := fmt.Sprintf(".docker-orkes/stacks/%s/%s", stack.Name, fileName)
	content, err := s.executor.ReadRemoteFile(r.Context(), node.Host, node.Port, node.User, s.privKey, remoteFilePath)
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

	data := map[string]interface{}{
		"PublicKeySSH":      s.pubKey,
		"Nodes":             nodes,
		"NodeCount":         len(nodes),
		"Stacks":            stacks,
		"StackCount":        len(stacks),
		"Deployments":       deployments,
		"DeploymentCount":   len(deployments),
		"GitMirrorURL":      gitMirrorURL,
		"GitMirrorBranch":   gitMirrorBranch,
		"GitMirrorStatus":   gitMirrorStatus,
		"GitMirrorLastSync": gitMirrorLastSync,
		"GitMirrorError":    gitMirrorError,
		"RunbookNotes":      runbookNotes,
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
	files := make(map[string][]byte)

	stacks, err := s.store.ListStacks()
	if err != nil {
		return nil, err
	}
	for _, st := range stacks {
		composePath := fmt.Sprintf("stacks/%s/docker-compose.yml", st.Name)
		files[composePath] = []byte(st.ComposeYAML)

		if st.DefaultEnv != "" {
			envPath := fmt.Sprintf("stacks/%s/.env.example", st.Name)
			files[envPath] = []byte(st.DefaultEnv)
		}
	}

	nodes, err := s.store.ListNodes()
	if err != nil {
		return nil, err
	}

	deployments, err := s.store.ListDeployments()
	if err != nil {
		return nil, err
	}
	for _, d := range deployments {
		depEnvPath := fmt.Sprintf("deployments/%s/%s.env", d.NodeName, d.StackName)
		files[depEnvPath] = []byte(d.EnvVars)
	}

	runbookNotes, _ := s.store.GetSetting("global_runbook_notes")
	if runbookNotes != "" {
		files["RUNBOOK.md"] = []byte(runbookNotes)
	}

	inventory := map[string]interface{}{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"nodes":        nodes,
		"stacks":       stacks,
		"deployments":  deployments,
	}
	invJSON, _ := json.MarshalIndent(inventory, "", "  ")
	files["inventory.json"] = invJSON

	return files, nil
}


func (s *Server) triggerGitSync(ctx context.Context) error {
	files, err := s.buildGitMirrorFiles()
	if err != nil {
		_ = s.store.SetSetting("git_mirror_status", "failed")
		_ = s.store.SetSetting("git_mirror_error", err.Error())
		return err
	}

	repoURL, _ := s.store.GetSetting("git_mirror_url")
	branch, _ := s.store.GetSetting("git_mirror_branch")
	if branch == "" {
		branch = "main"
	}

	syncErr := s.gitSyncer.Sync(ctx, s.gitMirrorDir, repoURL, branch, s.privKey, files)
	_ = s.store.SetSetting("git_mirror_last_sync", time.Now().UTC().Format(time.RFC3339))

	if syncErr != nil {
		_ = s.store.SetSetting("git_mirror_status", "failed")
		_ = s.store.SetSetting("git_mirror_error", syncErr.Error())
		return syncErr
	}

	_ = s.store.SetSetting("git_mirror_status", "synced")
	_ = s.store.SetSetting("git_mirror_error", "")
	return nil
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

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleSyncGitNow(w http.ResponseWriter, r *http.Request) {
	_ = s.triggerGitSync(r.Context())
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



