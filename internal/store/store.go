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

func (s *Store) DeleteNode(id string) error {
	_, err := s.db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	return err
}

