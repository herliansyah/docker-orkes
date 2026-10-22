package ssh

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type RemoteFileEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"`
	Mode    string `json:"mode"`
}

type Executor interface {
	TestConnection(ctx context.Context, host string, port int, user string, privKeyPEM string) (dockerVersion string, err error)
	RunCommand(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string) (output string, err error)
	RunCommandStream(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string, out io.Writer) error
	UploadFiles(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteDir string, files map[string][]byte) error
	ReadRemoteFile(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteFilePath string) ([]byte, error)
	ListRemoteFiles(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteDir string) ([]RemoteFileEntry, error)
	BootstrapKey(ctx context.Context, host string, port int, user string, password string, pubKey string) error
	RunInteractiveTerminal(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string, rows int, cols int, rw io.ReadWriter, resizeChan <-chan [2]int) error
}

type RealExecutor struct {
	KnownHostsPath string
}

func NewExecutor(knownHostsPath ...string) Executor {
	kh := ""
	if len(knownHostsPath) > 0 {
		kh = knownHostsPath[0]
	}
	return &RealExecutor{KnownHostsPath: kh}
}

func (e *RealExecutor) getHostKeyCallback() (ssh.HostKeyCallback, error) {
	if e.KnownHostsPath == "" {
		// ponytail: Insecure fallback when no known_hosts path is configured for agentless convenience
		return ssh.InsecureIgnoreHostKey(), nil
	}
	if _, err := os.Stat(e.KnownHostsPath); os.IsNotExist(err) {
		if dir := filepath.Dir(e.KnownHostsPath); dir != "" && dir != "." {
			_ = os.MkdirAll(dir, 0755)
		}
		_ = os.WriteFile(e.KnownHostsPath, []byte{}, 0600)
	}
	kh, err := knownhosts.New(e.KnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse known_hosts: %w", err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := kh(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			// Host is unknown: record fingerprint on first use (true TOFU)
			f, openErr := os.OpenFile(e.KnownHostsPath, os.O_APPEND|os.O_WRONLY, 0600)
			if openErr == nil {
				defer f.Close()
				line := knownhosts.Line([]string{hostname}, key)
				_, _ = f.WriteString(line + "\n")
			}
			return nil
		}
		return err // Key mismatch or verification error (blocks MITM attack)
	}, nil
}

func (e *RealExecutor) getSSHClient(ctx context.Context, host string, port int, user string, privKeyPEM string) (*ssh.Client, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("invalid ssh private key: %w", err)
	}

	hostKeyCb, err := e.getHostKeyCallback()
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: hostKeyCb,
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", addr, err)
	}

	c, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake failed with %s: %w", addr, err)
	}

	return ssh.NewClient(c, chans, reqs), nil
}

func (e *RealExecutor) TestConnection(ctx context.Context, host string, port int, user string, privKeyPEM string) (string, error) {
	client, err := e.getSSHClient(ctx, host, port, user, privKeyPEM)
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Test docker version to confirm both SSH login AND docker socket permissions
	cmd := "docker version --format 'Docker version {{.Server.Version}} (API {{.Server.APIVersion}})'"
	if err := runSessionWithContext(ctx, session, cmd); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		if strings.Contains(errMsg, "permission denied") {
			return "", fmt.Errorf("permission denied accessing Docker daemon: make sure user '%s' is in the 'docker' group: %s", user, errMsg)
		}
		return "", fmt.Errorf("docker check failed: %s", errMsg)
	}

	return strings.TrimSpace(stdout.String()), nil
}

func (e *RealExecutor) RunCommand(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string) (string, error) {
	client, err := e.getSSHClient(ctx, host, port, user, privKeyPEM)
	if err != nil {
		return "", err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("failed to create ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := runSessionWithContext(ctx, session, cmd); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return stdout.String(), fmt.Errorf("command failed: %s (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

func (e *RealExecutor) RunCommandStream(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string, out io.Writer) error {
	client, err := e.getSSHClient(ctx, host, port, user, privKeyPEM)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create ssh session: %w", err)
	}
	defer session.Close()

	session.Stdout = out
	session.Stderr = out

	if err := runSessionWithContext(ctx, session, cmd); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("command execution failed: %w", err)
	}

	return nil
}

func (e *RealExecutor) UploadFiles(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteDir string, files map[string][]byte) error {
	client, err := e.getSSHClient(ctx, host, port, user, privKeyPEM)
	if err != nil {
		return err
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return fmt.Errorf("failed to create sftp client: %w", err)
	}
	defer sftpClient.Close()

	// Ensure remote directory exists
	if err := sftpClient.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("failed to create remote directory %s: %w", remoteDir, err)
	}

	for filename, content := range files {
		targetPath := path.Join(remoteDir, filename)
		if err := sftpClient.MkdirAll(path.Dir(targetPath)); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", targetPath, err)
		}
		file, err := sftpClient.Create(targetPath)
		if err != nil {
			return fmt.Errorf("failed to create remote file %s: %w", targetPath, err)
		}

		if _, err := file.Write(content); err != nil {
			file.Close()
			return fmt.Errorf("failed to write to remote file %s: %w", targetPath, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("failed to close remote file %s: %w", targetPath, err)
		}
	}

	return nil
}

func (e *RealExecutor) ReadRemoteFile(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteFilePath string) ([]byte, error) {
	client, err := e.getSSHClient(ctx, host, port, user, privKeyPEM)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("failed to create sftp client: %w", err)
	}
	defer sftpClient.Close()

	file, err := sftpClient.Open(remoteFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open remote file %s: %w", remoteFilePath, err)
	}
	defer file.Close()

	// Bound file read to max 10MB to prevent memory exhaustion on unexpectedly large files
	content, err := io.ReadAll(io.LimitReader(file, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read remote file %s: %w", remoteFilePath, err)
	}

	return content, nil
}

func (e *RealExecutor) ListRemoteFiles(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteDir string) ([]RemoteFileEntry, error) {
	client, err := e.getSSHClient(ctx, host, port, user, privKeyPEM)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, fmt.Errorf("failed to create sftp client: %w", err)
	}
	defer sftpClient.Close()

	entries, err := sftpClient.ReadDir(remoteDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read remote dir %s: %w", remoteDir, err)
	}

	var files []RemoteFileEntry
	for _, entry := range entries {
		files = append(files, RemoteFileEntry{
			Name:    entry.Name(),
			Size:    entry.Size(),
			IsDir:   entry.IsDir(),
			ModTime: entry.ModTime().Format("Jan 02 15:04"),
			Mode:    entry.Mode().String(),
		})
	}
	return files, nil
}

func (e *RealExecutor) BootstrapKey(ctx context.Context, host string, port int, user string, password string, pubKey string) error {
	// Validate public key cryptographically before any remote interaction
	parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubKey))
	if err != nil {
		return fmt.Errorf("invalid ssh public key: %w", err)
	}
	cleanKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsedKey)))

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
			ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = password
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to dial %s: %w", addr, err)
	}
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		return fmt.Errorf("ssh password authentication failed: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create ssh session: %w", err)
	}
	defer session.Close()

	// Use base64 encoding to eliminate any possible shell injection vector
	b64Key := base64.StdEncoding.EncodeToString([]byte(cleanKey + "\n"))
	cmd := fmt.Sprintf(`mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && (echo %s | base64 -d | grep -qFf - ~/.ssh/authorized_keys || echo %s | base64 -d >> ~/.ssh/authorized_keys)`, b64Key, b64Key)

	var out bytes.Buffer
	session.Stdout = &out
	session.Stderr = &out
	if err := runSessionWithContext(ctx, session, cmd); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("failed to install ssh key on remote server: %w (output: %s)", err, strings.TrimSpace(out.String()))
	}

	return nil
}

func (e *RealExecutor) RunInteractiveTerminal(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string, rows int, cols int, rw io.ReadWriter, resizeChan <-chan [2]int) error {
	client, err := e.getSSHClient(ctx, host, port, user, privKeyPEM)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create ssh session: %w", err)
	}
	defer session.Close()

	if rows <= 0 {
		rows = 24
	}
	if cols <= 0 {
		cols = 80
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		return fmt.Errorf("failed to request pty: %w", err)
	}

	session.Stdin = rw
	session.Stdout = rw
	session.Stderr = rw

	if resizeChan != nil {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case sz, ok := <-resizeChan:
					if !ok {
						return
					}
					_ = session.WindowChange(sz[0], sz[1])
				}
			}
		}()
	}

	if cmd == "" {
		if err := session.Shell(); err != nil {
			return fmt.Errorf("failed to start remote shell: %w", err)
		}
	} else {
		if err := session.Start(cmd); err != nil {
			return fmt.Errorf("failed to start command: %w", err)
		}
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- session.Wait()
	}()

	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		return ctx.Err()
	case err := <-errChan:
		return err
	}
}

// runSessionWithContext executes a command in an SSH session while observing context cancellation.
// If the context is canceled or times out, SIGTERM is signaled and the session is closed immediately.
func runSessionWithContext(ctx context.Context, session *ssh.Session, cmd string) error {
	if err := session.Start(cmd); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- session.Wait()
	}()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		_ = session.Close()
		return ctx.Err()
	case err := <-done:
		return err
	}
}

