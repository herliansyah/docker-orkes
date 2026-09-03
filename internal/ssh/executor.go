package ssh

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type Executor interface {
	TestConnection(ctx context.Context, host string, port int, user string, privKeyPEM string) (dockerVersion string, err error)
	RunCommand(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string) (output string, err error)
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
