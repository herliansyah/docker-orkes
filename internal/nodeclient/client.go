package nodeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"docker-orkes/internal/ssh"
)

// ContainerInfo models summary container state returned by Docker daemon queries.
type ContainerInfo struct {
	ID     string `json:"id"`
	Names  string `json:"names"`
	Image  string `json:"image"`
	Status string `json:"status"`
	State  string `json:"state"`
}

// Config specifies credentials and connection properties to interact with a remote Docker host.
type Config struct {
	Host       string
	Port       int
	User       string
	PrivKeyPEM string
	Executor   ssh.Executor
}

// Client represents high-level operations executed on a remote node.
type Client interface {
	DeployStack(ctx context.Context, stackName string, files map[string][]byte) (output string, err error)
	UpStack(ctx context.Context, stackName string) (output string, err error)
	RestartStack(ctx context.Context, stackName string) (output string, err error)
	DownStack(ctx context.Context, stackName string) (output string, err error)
	ReadStackFile(ctx context.Context, stackName string, filename string) ([]byte, error)
	ListStackFiles(ctx context.Context, stackName string) ([]ssh.RemoteFileEntry, error)
	SaveStackFile(ctx context.Context, stackName string, filename string, content []byte) error
	GetTelemetry(ctx context.Context) (diskUsage string, containers []ContainerInfo, err error)
	GetContainerLogs(ctx context.Context, containerID string) (string, error)
	TestConnection(ctx context.Context) (dockerVersion string, err error)
	BootstrapKey(ctx context.Context, password string, pubKey string) error
}

type nodeClient struct {
	cfg Config
}

// New creates a Client backed by the specified Config and underlying ssh.Executor.
func New(cfg Config) Client {
	if cfg.Port <= 0 {
		cfg.Port = 22 // ponytail: default standard SSH port
	}
	if cfg.Executor == nil {
		cfg.Executor = ssh.NewExecutor() // ponytail: default fallback to real SSH executor
	}
	return &nodeClient{cfg: cfg}
}

func stackDirPath(stackName string) string {
	return fmt.Sprintf(".docker-orkes/stacks/%s", stackName)
}

func validateSegment(segment string) error {
	trimmed := strings.TrimSpace(segment)
	if trimmed == "" {
		return errors.New("segment cannot be empty")
	}
	if strings.Contains(trimmed, "..") || strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return errors.New("invalid path segment")
	}
	return nil
}

func validateFilename(filename string) error {
	trimmed := strings.TrimSpace(filename)
	if trimmed == "" {
		return errors.New("filename cannot be empty")
	}
	if strings.Contains(trimmed, "..") {
		return errors.New("path traversal '..' is not allowed")
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "\\") {
		return errors.New("absolute paths are not allowed")
	}
	clean := path.Clean(trimmed)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return errors.New("invalid filename path")
	}
	return nil
}

func (c *nodeClient) DeployStack(ctx context.Context, stackName string, files map[string][]byte) (string, error) {
	if err := validateSegment(stackName); err != nil {
		return "", fmt.Errorf("invalid stack name: %w", err)
	}

	stackDir := stackDirPath(stackName)
	if err := c.cfg.Executor.UploadFiles(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, stackDir, files); err != nil {
		return "", fmt.Errorf("failed to upload stack files: %w", err)
	}

	envArgs := ""
	if _, hasEnv := files[".env"]; hasEnv {
		envArgs = fmt.Sprintf(" --env-file %s/.env", stackDir)
	}

	composeFile := fmt.Sprintf("%s/docker-compose.yml", stackDir)

	pullCmd := fmt.Sprintf("docker compose -f %s%s pull", composeFile, envArgs)
	pullOut, err := c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, pullCmd)
	if err != nil {
		return pullOut, fmt.Errorf("docker compose pull failed: %w", err)
	}

	upCmd := fmt.Sprintf("docker compose -f %s%s up -d", composeFile, envArgs)
	upOut, err := c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, upCmd)
	if err != nil {
		return upOut, fmt.Errorf("docker compose up failed: %w", err)
	}

	if pullOut != "" {
		return pullOut + "\n" + upOut, nil
	}
	return upOut, nil
}

func (c *nodeClient) UpStack(ctx context.Context, stackName string) (string, error) {
	if err := validateSegment(stackName); err != nil {
		return "", fmt.Errorf("invalid stack name: %w", err)
	}
	stackDir := stackDirPath(stackName)
	cmd := fmt.Sprintf("docker compose -f %s/docker-compose.yml --env-file %s/.env up -d", stackDir, stackDir)
	return c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
}

func (c *nodeClient) RestartStack(ctx context.Context, stackName string) (string, error) {
	if err := validateSegment(stackName); err != nil {
		return "", fmt.Errorf("invalid stack name: %w", err)
	}
	cmd := fmt.Sprintf("docker compose -f %s/docker-compose.yml restart", stackDirPath(stackName))
	return c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
}

func (c *nodeClient) DownStack(ctx context.Context, stackName string) (string, error) {
	if err := validateSegment(stackName); err != nil {
		return "", fmt.Errorf("invalid stack name: %w", err)
	}
	cmd := fmt.Sprintf("docker compose -f %s/docker-compose.yml down", stackDirPath(stackName))
	return c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
}

func (c *nodeClient) ReadStackFile(ctx context.Context, stackName string, filename string) ([]byte, error) {
	if err := validateSegment(stackName); err != nil {
		return nil, fmt.Errorf("invalid stack name: %w", err)
	}
	if err := validateFilename(filename); err != nil {
		return nil, fmt.Errorf("invalid filename: %w", err)
	}
	remotePath := fmt.Sprintf("%s/%s", stackDirPath(stackName), filename)
	return c.cfg.Executor.ReadRemoteFile(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, remotePath)
}

func (c *nodeClient) ListStackFiles(ctx context.Context, stackName string) ([]ssh.RemoteFileEntry, error) {
	if err := validateSegment(stackName); err != nil {
		return nil, fmt.Errorf("invalid stack name: %w", err)
	}
	return c.cfg.Executor.ListRemoteFiles(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, stackDirPath(stackName))
}

func (c *nodeClient) SaveStackFile(ctx context.Context, stackName string, filename string, content []byte) error {
	if err := validateSegment(stackName); err != nil {
		return fmt.Errorf("invalid stack name: %w", err)
	}
	if err := validateFilename(filename); err != nil {
		return fmt.Errorf("invalid filename: %w", err)
	}
	files := map[string][]byte{
		filename: content,
	}
	return c.cfg.Executor.UploadFiles(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, stackDirPath(stackName), files)
}

func (c *nodeClient) GetTelemetry(ctx context.Context) (string, []ContainerInfo, error) {
	dfOut, err := c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, "df -h /")
	if err != nil {
		return "", nil, fmt.Errorf("failed to get disk usage: %w", err)
	}

	psOut, err := c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, "docker ps -a --format '{{json .}}'")
	if err != nil {
		return "", nil, fmt.Errorf("failed to get container list: %w", err)
	}

	diskUsage := parseDiskUsage(dfOut)
	containers := parseContainers(psOut)
	return diskUsage, containers, nil
}

func parseDiskUsage(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return strings.TrimSpace(out)
	}
	fields := strings.Fields(lines[1])
	if len(fields) >= 5 {
		return fields[4] // ponytail: Use% column from df -h / (e.g. "30%")
	}
	if len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return ""
}

func parseContainers(out string) []ContainerInfo {
	var list []ContainerInfo
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ci ContainerInfo
		if err := json.Unmarshal([]byte(line), &ci); err == nil {
			list = append(list, ci)
		}
	}
	return list
}

func (c *nodeClient) GetContainerLogs(ctx context.Context, containerID string) (string, error) {
	cleanID := strings.TrimSpace(containerID)
	if cleanID == "" || strings.ContainsAny(cleanID, " \t\n\r;|&`$><'\"\\") {
		return "", fmt.Errorf("invalid container ID: %q", containerID)
	}
	cmd := fmt.Sprintf("docker logs --tail 100 %s", cleanID)
	return c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
}

func (c *nodeClient) TestConnection(ctx context.Context) (string, error) {
	return c.cfg.Executor.TestConnection(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM)
}

func (c *nodeClient) BootstrapKey(ctx context.Context, password string, pubKey string) error {
	return c.cfg.Executor.BootstrapKey(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, password, pubKey)
}
