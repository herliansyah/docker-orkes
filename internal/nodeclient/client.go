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
	ID        string `json:"id"`
	Names     string `json:"names"`
	Image     string `json:"image"`
	Status    string `json:"status"`
	State     string `json:"state"`
	StackName string `json:"stack_name,omitempty"`
	Labels    string `json:"labels,omitempty"`
}

// NodeVolumeInfo models Docker volume information on a node.
type NodeVolumeInfo struct {
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Scope      string   `json:"scope"`
	Size       string   `json:"size,omitempty"`
	IsManaged  bool     `json:"is_managed"`
	StackName  string   `json:"stack_name,omitempty"`
	InUse      bool     `json:"in_use"`
	Containers []string `json:"containers,omitempty"`
}

// NodeNetworkInfo models Docker network information on a node.
type NodeNetworkInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Driver     string   `json:"driver"`
	Scope      string   `json:"scope"`
	IsSystem   bool     `json:"is_system"`
	IsManaged  bool     `json:"is_managed"`
	StackName  string   `json:"stack_name,omitempty"`
	InUse      bool     `json:"in_use"`
	Containers []string `json:"containers,omitempty"`
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
	DeployStack(ctx context.Context, stackName string, files map[string][]byte, pull ...bool) (output string, err error)
	UpStack(ctx context.Context, stackName string) (output string, err error)
	RestartStack(ctx context.Context, stackName string) (output string, err error)
	DownStack(ctx context.Context, stackName string) (output string, err error)
	ReadStackFile(ctx context.Context, stackName string, filename string) ([]byte, error)
	ListStackFiles(ctx context.Context, stackName string, subpath ...string) ([]ssh.RemoteFileEntry, error)
	SaveStackFile(ctx context.Context, stackName string, filename string, content []byte) error
	CreateStackDir(ctx context.Context, stackName string, dirPath string) error
	DeleteStackPath(ctx context.Context, stackName string, relPath string) error
	ExtractStackArchive(ctx context.Context, stackName string, archiveFilename string, targetDir string) error
	UploadStackFiles(ctx context.Context, stackName string, files map[string][]byte) error
	GetTelemetry(ctx context.Context) (diskUsage string, containers []ContainerInfo, err error)
	GetContainerLogs(ctx context.Context, containerID string) (string, error)
	StartContainer(ctx context.Context, containerID string) error
	StopContainer(ctx context.Context, containerID string) error
	RestartContainer(ctx context.Context, containerID string) error
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
	first := trimmed[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')) {
		return errors.New("path segment must start with alphanumeric character")
	}
	for _, c := range trimmed {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			return errors.New("invalid character in path segment")
		}
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
	for _, c := range trimmed {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == '/') {
			return errors.New("invalid characters in filename")
		}
	}
	clean := path.Clean(trimmed)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return errors.New("invalid filename path")
	}
	return nil
}

func validateSubpath(subpath string) (string, error) {
	trimmed := strings.TrimSpace(subpath)
	if trimmed == "" || trimmed == "." {
		return "", nil
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	if strings.Contains(trimmed, "..") {
		return "", errors.New("path traversal '..' is not allowed")
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", errors.New("absolute paths are not allowed")
	}
	for _, c := range trimmed {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == '/') {
			return "", errors.New("invalid characters in subpath")
		}
	}
	clean := path.Clean(trimmed)
	if clean == "." || clean == "" {
		return "", nil
	}
	if strings.HasPrefix(clean, "..") {
		return "", errors.New("invalid subpath")
	}
	return clean, nil
}

func (c *nodeClient) DeployStack(ctx context.Context, stackName string, files map[string][]byte, pull ...bool) (string, error) {
	if err := validateSegment(stackName); err != nil {
		return "", fmt.Errorf("invalid stack name: %w", err)
	}

	shouldPull := true
	if len(pull) > 0 {
		shouldPull = pull[0]
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

	pullOut := ""
	if shouldPull {
		pullCmd := fmt.Sprintf("docker compose -f %s%s pull", composeFile, envArgs)
		var err error
		pullOut, err = c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, pullCmd)
		if err != nil {
			return pullOut, fmt.Errorf("docker compose pull failed: %w", err)
		}
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
	// ponytail: Check if .env exists before passing --env-file to prevent compose failures on stacks without .env
	cmd := fmt.Sprintf("if [ -f %q/.env ]; then docker compose -f %q/docker-compose.yml --env-file %q/.env up -d; else docker compose -f %q/docker-compose.yml up -d; fi", stackDir, stackDir, stackDir, stackDir)
	return c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
}

func (c *nodeClient) RestartStack(ctx context.Context, stackName string) (string, error) {
	if err := validateSegment(stackName); err != nil {
		return "", fmt.Errorf("invalid stack name: %w", err)
	}
	stackDir := stackDirPath(stackName)
	// ponytail: Check if .env exists before passing --env-file to prevent compose failures on stacks without .env
	cmd := fmt.Sprintf("if [ -f %s/.env ]; then docker compose -f %s/docker-compose.yml --env-file %s/.env restart; else docker compose -f %s/docker-compose.yml restart; fi", stackDir, stackDir, stackDir, stackDir)
	return c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
}

func (c *nodeClient) DownStack(ctx context.Context, stackName string) (string, error) {
	if err := validateSegment(stackName); err != nil {
		return "", fmt.Errorf("invalid stack name: %w", err)
	}
	stackDir := stackDirPath(stackName)
	// ponytail: Check if .env exists before passing --env-file to prevent compose failures on stacks without .env
	cmd := fmt.Sprintf("if [ -f %s/.env ]; then docker compose -f %s/docker-compose.yml --env-file %s/.env down; else docker compose -f %s/docker-compose.yml down; fi", stackDir, stackDir, stackDir, stackDir)
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

func (c *nodeClient) ListStackFiles(ctx context.Context, stackName string, subpath ...string) ([]ssh.RemoteFileEntry, error) {
	if err := validateSegment(stackName); err != nil {
		return nil, fmt.Errorf("invalid stack name: %w", err)
	}
	targetDir := stackDirPath(stackName)
	if len(subpath) > 0 && subpath[0] != "" {
		cleanSub, err := validateSubpath(subpath[0])
		if err != nil {
			return nil, fmt.Errorf("invalid subpath: %w", err)
		}
		if cleanSub != "" {
			targetDir = fmt.Sprintf("%s/%s", targetDir, cleanSub)
		}
	}
	return c.cfg.Executor.ListRemoteFiles(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, targetDir)
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

func (c *nodeClient) CreateStackDir(ctx context.Context, stackName string, dirPath string) error {
	if err := validateSegment(stackName); err != nil {
		return fmt.Errorf("invalid stack name: %w", err)
	}
	cleanSub, err := validateSubpath(dirPath)
	if err != nil || cleanSub == "" {
		return fmt.Errorf("invalid directory path: %v", err)
	}
	fullPath := fmt.Sprintf("%s/%s", stackDirPath(stackName), cleanSub)
	cmd := fmt.Sprintf("mkdir -p %q", fullPath)
	_, err = c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
	return err
}

func (c *nodeClient) DeleteStackPath(ctx context.Context, stackName string, relPath string) error {
	if err := validateSegment(stackName); err != nil {
		return fmt.Errorf("invalid stack name: %w", err)
	}
	cleanRel, err := validateSubpath(relPath)
	if err != nil || cleanRel == "" {
		return fmt.Errorf("invalid path to delete: %v", err)
	}
	if cleanRel == "docker-compose.yml" {
		return errors.New("cannot delete docker-compose.yml: core deployment file is protected")
	}
	fullPath := fmt.Sprintf("%s/%s", stackDirPath(stackName), cleanRel)
	cmd := fmt.Sprintf("rm -rf -- %q", fullPath)
	_, err = c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
	return err
}

func (c *nodeClient) ExtractStackArchive(ctx context.Context, stackName string, archiveFilename string, targetDir string) error {
	if err := validateSegment(stackName); err != nil {
		return fmt.Errorf("invalid stack name: %w", err)
	}
	cleanArchive, err := validateSubpath(archiveFilename)
	if err != nil || cleanArchive == "" {
		return fmt.Errorf("invalid archive filename: %v", err)
	}
	cleanTarget, err := validateSubpath(targetDir)
	if err != nil {
		return fmt.Errorf("invalid target directory: %v", err)
	}

	stackRoot := stackDirPath(stackName)
	fullArchive := fmt.Sprintf("%s/%s", stackRoot, cleanArchive)
	destDir := stackRoot
	if cleanTarget != "" {
		destDir = fmt.Sprintf("%s/%s", stackRoot, cleanTarget)
	}

	lower := strings.ToLower(cleanArchive)
	var cmd string
	if strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar") || strings.HasSuffix(lower, ".tar.bz2") || strings.HasSuffix(lower, ".tar.xz") {
		cmd = fmt.Sprintf("mkdir -p %q && tar -xf %q -C %q", destDir, fullArchive, destDir)
	} else if strings.HasSuffix(lower, ".zip") {
		// ponytail: unzip with standard remote CLI tool
		cmd = fmt.Sprintf("mkdir -p %q && unzip -o %q -d %q", destDir, fullArchive, destDir)
	} else {
		return fmt.Errorf("unsupported archive format: %s (supported: .tar.gz, .tgz, .tar, .zip)", cleanArchive)
	}

	out, err := c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
	if err != nil {
		return fmt.Errorf("extraction failed: %w (output: %s)", err, out)
	}
	return nil
}

func (c *nodeClient) UploadStackFiles(ctx context.Context, stackName string, files map[string][]byte) error {
	if err := validateSegment(stackName); err != nil {
		return fmt.Errorf("invalid stack name: %w", err)
	}
	for f := range files {
		if err := validateFilename(f); err != nil {
			return fmt.Errorf("invalid file path %q: %w", f, err)
		}
	}
	return c.cfg.Executor.UploadFiles(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, stackDirPath(stackName), files)
}

func (c *nodeClient) GetTelemetry(ctx context.Context) (string, []ContainerInfo, error) {
	dfOut, err := c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, "df -h -P /")
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

type rawContainerJSON struct {
	ID          string `json:"ID"`
	IDLower     string `json:"id"`
	Names       string `json:"Names"`
	NamesLower  string `json:"names"`
	Image       string `json:"Image"`
	ImageLower  string `json:"image"`
	Status      string `json:"Status"`
	StatusLower string `json:"status"`
	State       string `json:"State"`
	StateLower  string `json:"state"`
	Labels      string `json:"Labels"`
	LabelsLower string `json:"labels"`
}

func (r rawContainerJSON) getID() string {
	if r.ID != "" {
		return r.ID
	}
	return r.IDLower
}
func (r rawContainerJSON) getNames() string {
	if r.Names != "" {
		return r.Names
	}
	return r.NamesLower
}
func (r rawContainerJSON) getImage() string {
	if r.Image != "" {
		return r.Image
	}
	return r.ImageLower
}
func (r rawContainerJSON) getStatus() string {
	if r.Status != "" {
		return r.Status
	}
	return r.StatusLower
}
func (r rawContainerJSON) getState() string {
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
func (r rawContainerJSON) getLabels() string {
	if r.Labels != "" {
		return r.Labels
	}
	return r.LabelsLower
}

func isValidLabelKey(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func parseLabels(labelsStr string) map[string]string {
	res := make(map[string]string)
	if labelsStr == "" {
		return res
	}
	parts := strings.Split(labelsStr, ",")
	var currentKey, currentVal string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if kv := strings.SplitN(trimmed, "=", 2); len(kv) == 2 && isValidLabelKey(kv[0]) {
			if currentKey != "" {
				res[currentKey] = currentVal
			}
			currentKey = kv[0]
			currentVal = kv[1]
		} else if currentKey != "" {
			currentVal += "," + trimmed
		}
	}
	if currentKey != "" {
		res[currentKey] = currentVal
	}
	return res
}

func parseContainers(out string) []ContainerInfo {
	var list []ContainerInfo
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw rawContainerJSON
		if err := json.Unmarshal([]byte(line), &raw); err == nil {
			labels := parseLabels(raw.getLabels())
			stackName := labels["com.docker.compose.project"]
			if stackName == "" {
				workingDir := labels["com.docker.compose.project.working_dir"]
				if workingDir != "" && strings.Contains(workingDir, ".docker-orkes/stacks") {
					stackName = path.Base(workingDir)
				}
			}

			list = append(list, ContainerInfo{
				ID:        raw.getID(),
				Names:     raw.getNames(),
				Image:     raw.getImage(),
				Status:    raw.getStatus(),
				State:     raw.getState(),
				StackName: stackName,
				Labels:    raw.getLabels(),
			})
		}
	}
	return list
}

// MatchesStack checks whether a container belongs to a specified stack name.
func (c ContainerInfo) MatchesStack(stackName string) bool {
	if c.StackName != "" {
		return strings.EqualFold(c.StackName, stackName)
	}
	cName := strings.TrimPrefix(strings.ToLower(c.Names), "/")
	sName := strings.ToLower(stackName)
	if cName == sName {
		return true
	}
	// Standard Docker Compose container name pattern: <stack>_<service>_<num> or <stack>-<service>-<num>
	return strings.HasPrefix(cName, sName+"_") || strings.HasPrefix(cName, sName+"-")
}

func validateContainerID(containerID string) (string, error) {
	cleanID := strings.TrimSpace(containerID)
	if cleanID == "" || strings.HasPrefix(cleanID, "-") || strings.ContainsAny(cleanID, " \t\n\r;|&`$><'\"\\") {
		return "", fmt.Errorf("invalid container ID: %q", containerID)
	}
	for _, c := range cleanID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.') {
			return "", fmt.Errorf("invalid container ID: %q", containerID)
		}
	}
	return cleanID, nil
}

func (c *nodeClient) GetContainerLogs(ctx context.Context, containerID string) (string, error) {
	cleanID, err := validateContainerID(containerID)
	if err != nil {
		return "", err
	}
	cmd := fmt.Sprintf("docker logs --tail 100 -- %s", cleanID)
	return c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
}

func (c *nodeClient) StartContainer(ctx context.Context, containerID string) error {
	cleanID, err := validateContainerID(containerID)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("docker start -- %s", cleanID)
	_, err = c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
	return err
}

func (c *nodeClient) StopContainer(ctx context.Context, containerID string) error {
	cleanID, err := validateContainerID(containerID)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("docker stop -- %s", cleanID)
	_, err = c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
	return err
}

func (c *nodeClient) RestartContainer(ctx context.Context, containerID string) error {
	cleanID, err := validateContainerID(containerID)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("docker restart -- %s", cleanID)
	_, err = c.cfg.Executor.RunCommand(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM, cmd)
	return err
}

func (c *nodeClient) TestConnection(ctx context.Context) (string, error) {
	return c.cfg.Executor.TestConnection(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, c.cfg.PrivKeyPEM)
}

func (c *nodeClient) BootstrapKey(ctx context.Context, password string, pubKey string) error {
	return c.cfg.Executor.BootstrapKey(ctx, c.cfg.Host, c.cfg.Port, c.cfg.User, password, pubKey)
}
