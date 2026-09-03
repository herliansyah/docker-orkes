package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"docker-orkes/internal/crypto"
)

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Recommended SQLite pragmas for single-writer concurrency
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		// WAL might not be supported in :memory: databases, ignore error for memory
	}

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to init schema: %w", err)
	}

	return s, nil
}

type Node struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	User          string    `json:"user"`
	Status        string    `json:"status"` // "unknown", "online", "offline"
	LastError     string    `json:"last_error"`
	DockerVersion string    `json:"docker_version"`
	CreatedAt     time.Time `json:"created_at"`
}

type Stack struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ComposeYAML string    `json:"compose_yaml"`
	DefaultEnv  string    `json:"default_env"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Deployment struct {
	ID            string    `json:"id"`
	StackID       string    `json:"stack_id"`
	NodeID        string    `json:"node_id"`
	StackName     string    `json:"stack_name,omitempty"`
	NodeName      string    `json:"node_name,omitempty"`
	EnvVars       string    `json:"env_vars"`
	Status        string    `json:"status"` // pending, deploying, running, stopped, failed
	DesiredHash   string    `json:"desired_hash"`
	LastLog       string    `json:"last_log"`
	DriftStatus   string    `json:"drift_status"` // synced, drifted, unknown
	DiffOutput    string    `json:"diff_output"`
	RemoteCompose string    `json:"remote_compose"`
	RemoteEnv     string    `json:"remote_env"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS system_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS sessions (
		token TEXT PRIMARY KEY,
		created_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		host TEXT NOT NULL,
		port INTEGER NOT NULL DEFAULT 22,
		user TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'unknown',
		last_error TEXT NOT NULL DEFAULT '',
		docker_version TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS stacks (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		compose_yaml TEXT NOT NULL,
		default_env TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS deployments (
		id TEXT PRIMARY KEY,
		stack_id TEXT NOT NULL REFERENCES stacks(id) ON DELETE CASCADE,
		node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
		env_vars TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'pending',
		desired_hash TEXT NOT NULL DEFAULT '',
		last_log TEXT NOT NULL DEFAULT '',
		drift_status TEXT NOT NULL DEFAULT 'unknown',
		diff_output TEXT NOT NULL DEFAULT '',
		remote_compose TEXT NOT NULL DEFAULT '',
		remote_env TEXT NOT NULL DEFAULT '',
		updated_at DATETIME NOT NULL
	);
	`
	_, err := s.db.Exec(schema)
	return err
}



func (s *Store) GetSetting(key string) (string, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, key).Scan(&val)
	if err != nil {
		return "", err
	}
	return val, nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO system_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

func (s *Store) CreateSession(token string) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token, created_at) VALUES (?, ?)`, token, time.Now().UTC())
	return err
}

func (s *Store) ValidateSession(token string) bool {
	if token == "" {
		return false
	}
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token = ?`, token).Scan(&count)
	return err == nil && count > 0
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// EnsureSSHKeyPair retrieves the existing SSH keypair or creates a new one idempotently.
func (s *Store) EnsureSSHKeyPair() (publicKey string, privateKey string, err error) {
	pub, errPub := s.GetSetting("ssh_public_key")
	priv, errPriv := s.GetSetting("ssh_private_key")

	if errPub == nil && errPriv == nil && pub != "" && priv != "" {
		return pub, priv, nil
	}

	kp, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate keypair: %w", err)
	}

	if err := s.SetSetting("ssh_public_key", kp.PublicKeySSH); err != nil {
		return "", "", err
	}
	if err := s.SetSetting("ssh_private_key", kp.PrivateKeyPEM); err != nil {
		return "", "", err
	}

	return kp.PublicKeySSH, kp.PrivateKeyPEM, nil
}

func (s *Store) CreateNode(n Node) error {
	_, err := s.db.Exec(`
		INSERT INTO nodes (id, name, host, port, user, status, last_error, docker_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, n.ID, n.Name, n.Host, n.Port, n.User, n.Status, n.LastError, n.DockerVersion, n.CreatedAt)
	return err
}

func (s *Store) ListNodes() ([]Node, error) {
	rows, err := s.db.Query(`
		SELECT id, name, host, port, user, status, last_error, docker_version, created_at
		FROM nodes ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.User, &n.Status, &n.LastError, &n.DockerVersion, &n.CreatedAt); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func (s *Store) GetNode(id string) (*Node, error) {
	var n Node
	err := s.db.QueryRow(`
		SELECT id, name, host, port, user, status, last_error, docker_version, created_at
		FROM nodes WHERE id = ?
	`, id).Scan(&n.ID, &n.Name, &n.Host, &n.Port, &n.User, &n.Status, &n.LastError, &n.DockerVersion, &n.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (s *Store) UpdateNodeStatus(id, status, lastError, dockerVersion string) error {
	_, err := s.db.Exec(`
		UPDATE nodes SET status = ?, last_error = ?, docker_version = ? WHERE id = ?
	`, status, lastError, dockerVersion, id)
	return err
}

func (s *Store) UpdateNode(n Node) error {
	_, err := s.db.Exec(`
		UPDATE nodes SET name = ?, host = ?, port = ?, user = ? WHERE id = ?
	`, n.Name, n.Host, n.Port, n.User, n.ID)
	return err
}

func (s *Store) DeleteNode(id string) error {
	_, err := s.db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	return err
}


func (s *Store) CreateStack(st Stack) error {
	_, err := s.db.Exec(`
		INSERT INTO stacks (id, name, compose_yaml, default_env, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, st.ID, st.Name, st.ComposeYAML, st.DefaultEnv, st.CreatedAt, st.UpdatedAt)
	return err
}

func (s *Store) ListStacks() ([]Stack, error) {
	rows, err := s.db.Query(`
		SELECT id, name, compose_yaml, default_env, created_at, updated_at
		FROM stacks ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stacks []Stack
	for rows.Next() {
		var st Stack
		if err := rows.Scan(&st.ID, &st.Name, &st.ComposeYAML, &st.DefaultEnv, &st.CreatedAt, &st.UpdatedAt); err != nil {
			return nil, err
		}
		stacks = append(stacks, st)
	}
	return stacks, rows.Err()
}

func (s *Store) GetStack(id string) (*Stack, error) {
	var st Stack
	err := s.db.QueryRow(`
		SELECT id, name, compose_yaml, default_env, created_at, updated_at
		FROM stacks WHERE id = ?
	`, id).Scan(&st.ID, &st.Name, &st.ComposeYAML, &st.DefaultEnv, &st.CreatedAt, &st.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) UpdateStack(st Stack) error {
	_, err := s.db.Exec(`
		UPDATE stacks SET name = ?, compose_yaml = ?, default_env = ?, updated_at = ?
		WHERE id = ?
	`, st.Name, st.ComposeYAML, st.DefaultEnv, st.UpdatedAt, st.ID)
	return err
}

func (s *Store) DeleteStack(id string) error {
	_, err := s.db.Exec(`DELETE FROM stacks WHERE id = ?`, id)
	return err
}

func (s *Store) CreateDeployment(d Deployment) error {
	if d.DriftStatus == "" {
		d.DriftStatus = "unknown"
	}
	_, err := s.db.Exec(`
		INSERT INTO deployments (id, stack_id, node_id, env_vars, status, desired_hash, last_log, drift_status, diff_output, remote_compose, remote_env, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, d.ID, d.StackID, d.NodeID, d.EnvVars, d.Status, d.DesiredHash, d.LastLog, d.DriftStatus, d.DiffOutput, d.RemoteCompose, d.RemoteEnv, d.UpdatedAt)
	return err
}

func (s *Store) ListDeployments() ([]Deployment, error) {
	query := `
		SELECT d.id, d.stack_id, d.node_id, s.name, n.name, d.env_vars, d.status, d.desired_hash, d.last_log, d.drift_status, d.diff_output, d.remote_compose, d.remote_env, d.updated_at
		FROM deployments d
		JOIN stacks s ON d.stack_id = s.id
		JOIN nodes n ON d.node_id = n.id
		ORDER BY d.updated_at DESC
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var deps []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.StackID, &d.NodeID, &d.StackName, &d.NodeName, &d.EnvVars, &d.Status, &d.DesiredHash, &d.LastLog, &d.DriftStatus, &d.DiffOutput, &d.RemoteCompose, &d.RemoteEnv, &d.UpdatedAt); err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, rows.Err()
}

func (s *Store) GetDeployment(id string) (*Deployment, error) {
	query := `
		SELECT d.id, d.stack_id, d.node_id, s.name, n.name, d.env_vars, d.status, d.desired_hash, d.last_log, d.drift_status, d.diff_output, d.remote_compose, d.remote_env, d.updated_at
		FROM deployments d
		JOIN stacks s ON d.stack_id = s.id
		JOIN nodes n ON d.node_id = n.id
		WHERE d.id = ?
	`
	var d Deployment
	err := s.db.QueryRow(query, id).Scan(&d.ID, &d.StackID, &d.NodeID, &d.StackName, &d.NodeName, &d.EnvVars, &d.Status, &d.DesiredHash, &d.LastLog, &d.DriftStatus, &d.DiffOutput, &d.RemoteCompose, &d.RemoteEnv, &d.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) UpdateDeploymentStatus(id, status, lastLog string) error {
	_, err := s.db.Exec(`
		UPDATE deployments SET status = ?, last_log = ?, updated_at = ? WHERE id = ?
	`, status, lastLog, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateDeploymentDesiredHash(id, hash string) error {
	_, err := s.db.Exec(`
		UPDATE deployments SET desired_hash = ?, drift_status = 'synced', diff_output = '', updated_at = ? WHERE id = ?
	`, hash, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateDeploymentDrift(id, driftStatus, diffOutput, remoteCompose, remoteEnv string) error {
	_, err := s.db.Exec(`
		UPDATE deployments SET drift_status = ?, diff_output = ?, remote_compose = ?, remote_env = ?, updated_at = ? WHERE id = ?
	`, driftStatus, diffOutput, remoteCompose, remoteEnv, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateDeploymentEnvAndHash(id, envVars, hash string) error {
	_, err := s.db.Exec(`
		UPDATE deployments SET env_vars = ?, desired_hash = ?, drift_status = 'synced', diff_output = '', updated_at = ? WHERE id = ?
	`, envVars, hash, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateDeploymentEnv(id, envVars string) error {
	_, err := s.db.Exec(`
		UPDATE deployments SET env_vars = ?, updated_at = ? WHERE id = ?
	`, envVars, time.Now().UTC(), id)
	return err
}

func (s *Store) DeleteDeployment(id string) error {
	_, err := s.db.Exec(`DELETE FROM deployments WHERE id = ?`, id)
	return err
}





