package ssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"path"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
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
}




type RealExecutor struct{}

func NewExecutor() Executor {
	return &RealExecutor{}
}

func (e *RealExecutor) getSSHClient(ctx context.Context, host string, port int, user string, privKeyPEM string) (*ssh.Client, error) {
	signer, err := ssh.ParsePrivateKey([]byte(privKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("invalid ssh private key: %w", err)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // ponytail: TOFU / agentless convenience, ceiling: strict host key checking with known_hosts
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
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
	if err := session.Run(cmd); err != nil {
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

	if err := session.Run(cmd); err != nil {
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

	if err := session.Run(cmd); err != nil {
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

	content, err := io.ReadAll(file)
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
	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
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

	cleanKey := strings.TrimSpace(pubKey)
	cmd := fmt.Sprintf(`mkdir -p ~/.ssh && chmod 700 ~/.ssh && touch ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys && grep -qF '%s' ~/.ssh/authorized_keys || echo '%s' >> ~/.ssh/authorized_keys`, cleanKey, cleanKey)

	out, err := session.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("failed to install ssh key on remote server: %w (output: %s)", err, string(out))
	}

	return nil
}


