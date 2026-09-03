package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"docker-orkes/internal/diff"
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
}

type Server struct {
	cfg       Config
	store     *store.Store
	executor  ssh.Executor
	templates *template.Template
	pubKey    string
	privKey   string
}

func New(cfg Config) (*Server, error) {
	if cfg.MasterPassword == "" {
		cfg.MasterPassword = "admin" // ponytail: sensible default for zero-config quickstart, ceiling: user should provide strong password via env
	}
	if cfg.Executor == nil {
		cfg.Executor = ssh.NewExecutor()
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
		executor:  cfg.Executor,
		templates: tmpl,
		pubKey:    pubKey,
		privKey:   privKey,
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
	mux.Handle("POST /nodes/{id}/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteNode)))

	mux.Handle("GET /stacks", s.requireAuth(http.HandlerFunc(s.handleListStacks)))
	mux.Handle("POST /stacks", s.requireAuth(http.HandlerFunc(s.handleCreateStack)))
	mux.Handle("POST /stacks/{id}/update", s.requireAuth(http.HandlerFunc(s.handleUpdateStack)))
	mux.Handle("POST /stacks/{id}/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteStack)))

	mux.Handle("GET /deployments", s.requireAuth(http.HandlerFunc(s.handleListDeployments)))
	mux.Handle("POST /deployments", s.requireAuth(http.HandlerFunc(s.handleCreateDeployment)))
	mux.Handle("POST /deployments/{id}/up", s.requireAuth(http.HandlerFunc(s.handleUpDeployment)))
	mux.Handle("POST /deployments/{id}/restart", s.requireAuth(http.HandlerFunc(s.handleRestartDeployment)))
	mux.Handle("POST /deployments/{id}/down", s.requireAuth(http.HandlerFunc(s.handleDownDeployment)))
	mux.Handle("POST /deployments/{id}/delete", s.requireAuth(http.HandlerFunc(s.handleDeleteDeployment)))

	mux.Handle("POST /deployments/{id}/drift/check", s.requireAuth(http.HandlerFunc(s.handleCheckDrift)))
	mux.Handle("POST /deployments/{id}/drift/reconcile", s.requireAuth(http.HandlerFunc(s.handleReconcileDrift)))
	mux.Handle("POST /deployments/{id}/drift/adopt", s.requireAuth(http.HandlerFunc(s.handleAdoptDrift)))

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

	node := store.Node{
		ID:        generateToken(8),
		Name:      name,
		Host:      host,
		Port:      port,
		User:      user,
		Status:    "unknown",
		CreatedAt: time.Now().UTC(),
	}

	if err := s.store.CreateNode(node); err != nil {
		http.Error(w, "Failed to create node: "+err.Error(), http.StatusInternalServerError)
		return
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

	if stackID == "" || nodeID == "" {
		http.Error(w, "stack_id and node_id are required", http.StatusBadRequest)
		return
	}

	dep := store.Deployment{
		ID:        generateToken(8),
		StackID:   stackID,
		NodeID:    nodeID,
		EnvVars:   envVars,
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

	if err := s.executor.UploadFiles(ctx, node.Host, node.Port, node.User, s.privKey, remoteDir, files); err != nil {
		_ = s.store.UpdateDeploymentStatus(dep.ID, "failed", err.Error())
		return fmt.Errorf("failed to upload files: %w", err)
	}

	cmd := fmt.Sprintf("docker compose -f %s/docker-compose.yml --env-file %s/.env up -d", remoteDir, remoteDir)
	out, err := s.executor.RunCommand(ctx, node.Host, node.Port, node.User, s.privKey, cmd)
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

	data := map[string]interface{}{
		"PublicKeySSH":    s.pubKey,
		"Nodes":           nodes,
		"NodeCount":       len(nodes),
		"Stacks":          stacks,
		"StackCount":      len(stacks),
		"Deployments":     deployments,
		"DeploymentCount": len(deployments),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates.ExecuteTemplate(w, "dashboard.html", data)
}

func generateToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}


