package nodeclient

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"docker-orkes/internal/ssh"
)

type mockExecutor struct {
	mu            sync.Mutex
	commands      []string
	uploaded      map[string]map[string][]byte
	runCmdFunc    func(ctx context.Context, host string, port int, user string, privKey string, cmd string) (string, error)
	testConnFunc  func(ctx context.Context, host string, port int, user string, privKey string) (string, error)
	bootKeyFunc   func(ctx context.Context, host string, port int, user string, password string, pubKey string) error
	readFileFunc  func(ctx context.Context, host string, port int, user string, privKey string, remoteFilePath string) ([]byte, error)
	listFilesFunc func(ctx context.Context, host string, port int, user string, privKey string, remoteDir string) ([]ssh.RemoteFileEntry, error)
}

func (m *mockExecutor) TestConnection(ctx context.Context, host string, port int, user string, privKeyPEM string) (string, error) {
	if m.testConnFunc != nil {
		return m.testConnFunc(ctx, host, port, user, privKeyPEM)
	}
	return "Docker version 27.2.0", nil
}

func (m *mockExecutor) RunCommand(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string) (string, error) {
	m.mu.Lock()
	m.commands = append(m.commands, cmd)
	m.mu.Unlock()

	if m.runCmdFunc != nil {
		return m.runCmdFunc(ctx, host, port, user, privKeyPEM, cmd)
	}
	return "ok", nil
}

func (m *mockExecutor) RunCommandStream(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string, out io.Writer) error {
	m.mu.Lock()
	m.commands = append(m.commands, cmd)
	m.mu.Unlock()
	return nil
}

func (m *mockExecutor) UploadFiles(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteDir string, files map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploaded == nil {
		m.uploaded = make(map[string]map[string][]byte)
	}
	if m.uploaded[remoteDir] == nil {
		m.uploaded[remoteDir] = make(map[string][]byte)
	}
	for k, v := range files {
		m.uploaded[remoteDir][k] = v
	}
	return nil
}

func (m *mockExecutor) ReadRemoteFile(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteFilePath string) ([]byte, error) {
	if m.readFileFunc != nil {
		return m.readFileFunc(ctx, host, port, user, privKeyPEM, remoteFilePath)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for dir, files := range m.uploaded {
		for f, content := range files {
			if dir+"/"+f == remoteFilePath {
				return content, nil
			}
		}
	}
	return nil, errors.New("file not found")
}

func (m *mockExecutor) ListRemoteFiles(ctx context.Context, host string, port int, user string, privKeyPEM string, remoteDir string) ([]ssh.RemoteFileEntry, error) {
	if m.listFilesFunc != nil {
		return m.listFilesFunc(ctx, host, port, user, privKeyPEM, remoteDir)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []ssh.RemoteFileEntry
	if files, ok := m.uploaded[remoteDir]; ok {
		for name, content := range files {
			entries = append(entries, ssh.RemoteFileEntry{
				Name: name,
				Size: int64(len(content)),
			})
		}
	}
	return entries, nil
}

func (m *mockExecutor) BootstrapKey(ctx context.Context, host string, port int, user string, password string, pubKey string) error {
	if m.bootKeyFunc != nil {
		return m.bootKeyFunc(ctx, host, port, user, password, pubKey)
	}
	return nil
}

func (m *mockExecutor) RunInteractiveTerminal(ctx context.Context, host string, port int, user string, privKeyPEM string, cmd string, rows int, cols int, rw io.ReadWriter, resizeChan <-chan [2]int) error {
	return nil
}

func TestClient_DeployStack(t *testing.T) {
	mock := &mockExecutor{}
	cli := New(Config{
		Host:       "192.168.1.100",
		Port:       22,
		User:       "ubuntu",
		PrivKeyPEM: "dummy-key",
		Executor:   mock,
	})

	files := map[string][]byte{
		"docker-compose.yml": []byte("services:\n  web:\n    image: nginx\n"),
		".env":               []byte("PORT=8080\n"),
	}

	out, err := cli.DeployStack(context.Background(), "web-app", files)
	if err != nil {
		t.Fatalf("DeployStack returned error: %v", err)
	}
	if out == "" {
		t.Fatalf("expected non-empty output")
	}

	expectedDir := ".docker-orkes/stacks/web-app"
	uploaded := mock.uploaded[expectedDir]
	if uploaded == nil {
		t.Fatalf("expected files uploaded to %q", expectedDir)
	}
	if string(uploaded["docker-compose.yml"]) != string(files["docker-compose.yml"]) {
		t.Errorf("mismatched docker-compose.yml content")
	}
	if string(uploaded[".env"]) != string(files[".env"]) {
		t.Errorf("mismatched .env content")
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()

	var hasPull, hasUp bool
	for _, cmd := range mock.commands {
		if strings.Contains(cmd, "docker compose") && strings.Contains(cmd, "pull") && strings.Contains(cmd, expectedDir) {
			hasPull = true
		}
		if strings.Contains(cmd, "docker compose") && strings.Contains(cmd, "up -d") && strings.Contains(cmd, expectedDir) && strings.Contains(cmd, "--env-file") {
			hasUp = true
		}
	}
	if !hasPull {
		t.Errorf("expected docker compose pull command executed, commands: %v", mock.commands)
	}
	if !hasUp {
		t.Errorf("expected docker compose up -d command with env file executed, commands: %v", mock.commands)
	}
}

func TestClient_DeployStackWithoutPull(t *testing.T) {
	mock := &mockExecutor{}
	cli := New(Config{
		Host:     "192.168.1.100",
		Port:     22,
		User:     "ubuntu",
		Executor: mock,
	})

	files := map[string][]byte{
		"docker-compose.yml": []byte("services:\n  app:\n    image: my-local-app:latest\n"),
	}

	_, err := cli.DeployStack(context.Background(), "local-stack", files, false)
	if err != nil {
		t.Fatalf("DeployStack failed: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()

	for _, cmd := range mock.commands {
		if strings.Contains(cmd, "docker compose") && strings.Contains(cmd, "pull") {
			t.Errorf("unexpected docker compose pull command executed when pull=false: %s", cmd)
		}
	}
}

func TestClient_RestartAndDownStack(t *testing.T) {
	mock := &mockExecutor{}
	cli := New(Config{
		Host:     "192.168.1.100",
		Port:     22,
		User:     "ubuntu",
		Executor: mock,
	})

	ctx := context.Background()

	if _, err := cli.RestartStack(ctx, "web-app"); err != nil {
		t.Fatalf("RestartStack failed: %v", err)
	}
	if _, err := cli.DownStack(ctx, "web-app"); err != nil {
		t.Fatalf("DownStack failed: %v", err)
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()

	var hasRestart, hasDown bool
	for _, cmd := range mock.commands {
		if strings.Contains(cmd, "docker compose -f .docker-orkes/stacks/web-app/docker-compose.yml restart") {
			hasRestart = true
		}
		if strings.Contains(cmd, "docker compose -f .docker-orkes/stacks/web-app/docker-compose.yml down") {
			hasDown = true
		}
	}
	if !hasRestart {
		t.Errorf("missing restart command in %v", mock.commands)
	}
	if !hasDown {
		t.Errorf("missing down command in %v", mock.commands)
	}
}

func TestClient_ContainerLifecycleActions(t *testing.T) {
	mock := &mockExecutor{}
	cli := New(Config{
		Host:     "192.168.1.100",
		Port:     22,
		User:     "ubuntu",
		Executor: mock,
	})

	ctx := context.Background()

	// 1. Valid commands
	if err := cli.StartContainer(ctx, "c101_web"); err != nil {
		t.Fatalf("StartContainer failed: %v", err)
	}
	if err := cli.StopContainer(ctx, "c101_web"); err != nil {
		t.Fatalf("StopContainer failed: %v", err)
	}
	if err := cli.RestartContainer(ctx, "c101_web"); err != nil {
		t.Fatalf("RestartContainer failed: %v", err)
	}

	expectedCmds := []string{
		"docker start -- c101_web",
		"docker stop -- c101_web",
		"docker restart -- c101_web",
	}
	for i, exp := range expectedCmds {
		if mock.commands[i] != exp {
			t.Errorf("cmd %d: expected %q, got %q", i, exp, mock.commands[i])
		}
	}

	// 2. Reject injection attempts
	invalidIDs := []string{
		"-c101",
		"c101;rm -rf /",
		"c101$(whoami)",
		"c101`id`",
		"",
	}
	for _, badID := range invalidIDs {
		if err := cli.StartContainer(ctx, badID); err == nil {
			t.Errorf("expected error for bad container ID %q, got nil", badID)
		}
	}
}

func TestClient_GetTelemetry(t *testing.T) {
	mock := &mockExecutor{
		runCmdFunc: func(ctx context.Context, host string, port int, user string, privKey string, cmd string) (string, error) {
			if strings.HasPrefix(cmd, "df") {
				return "Filesystem      Size  Used Avail Use% Mounted on\n/dev/sda1        40G   15G   25G  38% /\n", nil
			}
			if strings.Contains(cmd, "docker ps") {
				return `{"id":"c101","names":"web-app-1","image":"nginx:alpine","status":"Up 2 hours","state":"running"}
{"id":"c102","names":"redis-db","image":"redis:7","status":"Exited (0) 5 mins ago","state":"exited"}
`, nil
			}
			return "", nil
		},
	}

	cli := New(Config{
		Host:     "192.168.1.100",
		Port:     22,
		User:     "ubuntu",
		Executor: mock,
	})

	diskUsage, containers, err := cli.GetTelemetry(context.Background())
	if err != nil {
		t.Fatalf("GetTelemetry failed: %v", err)
	}

	if diskUsage != "38%" {
		t.Errorf("expected diskUsage '38%%', got %q", diskUsage)
	}

	if len(containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(containers))
	}

	expected0 := ContainerInfo{
		ID:     "c101",
		Names:  "web-app-1",
		Image:  "nginx:alpine",
		Status: "Up 2 hours",
		State:  "running",
	}
	if containers[0] != expected0 {
		t.Errorf("container 0 mismatch: got %+v, expected %+v", containers[0], expected0)
	}

	expected1 := ContainerInfo{
		ID:     "c102",
		Names:  "redis-db",
		Image:  "redis:7",
		Status: "Exited (0) 5 mins ago",
		State:  "exited",
	}
	if containers[1] != expected1 {
		t.Errorf("container 1 mismatch: got %+v, expected %+v", containers[1], expected1)
	}
}

func TestClient_PathSanitation(t *testing.T) {
	mock := &mockExecutor{}
	cli := New(Config{
		Host:     "192.168.1.100",
		Port:     22,
		User:     "ubuntu",
		Executor: mock,
	})

	ctx := context.Background()

	traversalFilenames := []string{
		"../evil.txt",
		"../../etc/passwd",
		"sub/../../secret",
		"/etc/shadow",
		"..",
		"",
		"test$(whoami)",
		"test;id;",
		"test'quote",
		"test\"quote",
	}

	for _, fname := range traversalFilenames {
		t.Run("ReadStackFile_"+fname, func(t *testing.T) {
			_, err := cli.ReadStackFile(ctx, "app", fname)
			if err == nil {
				t.Errorf("expected error for ReadStackFile with filename %q, got nil", fname)
			}
		})

		t.Run("SaveStackFile_"+fname, func(t *testing.T) {
			err := cli.SaveStackFile(ctx, "app", fname, []byte("data"))
			if err == nil {
				t.Errorf("expected error for SaveStackFile with filename %q, got nil", fname)
			}
		})
	}

	traversalStacks := []string{
		"../evil",
		"../../root",
		"/root",
		"stack/sub",
		"",
		"-bad-name",
		"stack with space",
		"stack;id",
		"stack$(whoami)",
	}

	for _, sname := range traversalStacks {
		t.Run("InvalidStack_"+sname, func(t *testing.T) {
			if _, err := cli.DeployStack(ctx, sname, nil); err == nil {
				t.Errorf("expected error for DeployStack with stack name %q", sname)
			}
			if _, err := cli.RestartStack(ctx, sname); err == nil {
				t.Errorf("expected error for RestartStack with stack name %q", sname)
			}
			if _, err := cli.DownStack(ctx, sname); err == nil {
				t.Errorf("expected error for DownStack with stack name %q", sname)
			}
			if _, err := cli.ListStackFiles(ctx, sname); err == nil {
				t.Errorf("expected error for ListStackFiles with stack name %q", sname)
			}
			if _, err := cli.ReadStackFile(ctx, sname, "docker-compose.yml"); err == nil {
				t.Errorf("expected error for ReadStackFile with stack name %q", sname)
			}
			if err := cli.SaveStackFile(ctx, sname, "docker-compose.yml", []byte("data")); err == nil {
				t.Errorf("expected error for SaveStackFile with stack name %q", sname)
			}
		})
	}
}

func TestClient_FileOpsAndDelegations(t *testing.T) {
	mock := &mockExecutor{}
	cli := New(Config{
		Host:       "192.168.1.100",
		Port:       22,
		User:       "ubuntu",
		PrivKeyPEM: "dummy-key",
		Executor:   mock,
	})

	ctx := context.Background()

	// 1. Save and Read stack file
	if err := cli.SaveStackFile(ctx, "my-stack", "config.json", []byte(`{"key":"val"}`)); err != nil {
		t.Fatalf("SaveStackFile failed: %v", err)
	}

	data, err := cli.ReadStackFile(ctx, "my-stack", "config.json")
	if err != nil {
		t.Fatalf("ReadStackFile failed: %v", err)
	}
	if string(data) != `{"key":"val"}` {
		t.Errorf("expected file content `{\"key\":\"val\"}`, got %q", string(data))
	}

	// 2. List stack files
	files, err := cli.ListStackFiles(ctx, "my-stack")
	if err != nil {
		t.Fatalf("ListStackFiles failed: %v", err)
	}
	if len(files) != 1 || files[0].Name != "config.json" {
		t.Errorf("unexpected files listed: %+v", files)
	}

	// 3. Container logs
	if _, err := cli.GetContainerLogs(ctx, "c123; rm -rf /"); err == nil {
		t.Errorf("expected error for invalid container ID injection")
	}
	if _, err := cli.GetContainerLogs(ctx, "-f"); err == nil {
		t.Errorf("expected error for container ID starting with hyphen")
	}
	logs, err := cli.GetContainerLogs(ctx, "c123")
	if err != nil {
		t.Fatalf("GetContainerLogs failed: %v", err)
	}
	if logs != "ok" {
		t.Errorf("expected 'ok', got %q", logs)
	}

	// 4. TestConnection
	ver, err := cli.TestConnection(ctx)
	if err != nil || !strings.Contains(ver, "Docker version") {
		t.Errorf("unexpected TestConnection result: ver=%q, err=%v", ver, err)
	}

	// 5. BootstrapKey
	if err := cli.BootstrapKey(ctx, "password", "ssh-rsa AAA..."); err != nil {
		t.Errorf("BootstrapKey failed: %v", err)
	}
}

func TestClient_FileManagementFeatures(t *testing.T) {
	var executedCmds []string
	mock := &mockExecutor{
		runCmdFunc: func(ctx context.Context, host string, port int, user string, privKey string, cmd string) (string, error) {
			executedCmds = append(executedCmds, cmd)
			return "success", nil
		},
	}
	cli := New(Config{
		Host:       "10.0.0.1",
		Port:       22,
		User:       "admin",
		PrivKeyPEM: "key",
		Executor:   mock,
	})

	ctx := context.Background()

	// 1. CreateStackDir
	if err := cli.CreateStackDir(ctx, "app", "conf.d/sub"); err != nil {
		t.Fatalf("CreateStackDir failed: %v", err)
	}
	if len(executedCmds) == 0 || !strings.Contains(executedCmds[0], "mkdir -p \".docker-orkes/stacks/app/conf.d/sub\"") {
		t.Errorf("unexpected mkdir command: %v", executedCmds)
	}

	// 2. DeleteStackPath with protection
	err := cli.DeleteStackPath(ctx, "app", "docker-compose.yml")
	if err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("expected error deleting protected docker-compose.yml, got %v", err)
	}

	// Delete non-protected file
	if err := cli.DeleteStackPath(ctx, "app", "conf.d/old.conf"); err != nil {
		t.Fatalf("DeleteStackPath failed: %v", err)
	}
	if !strings.Contains(executedCmds[len(executedCmds)-1], "rm -rf -- \".docker-orkes/stacks/app/conf.d/old.conf\"") {
		t.Errorf("unexpected rm command: %v", executedCmds[len(executedCmds)-1])
	}

	// 3. ExtractStackArchive
	if err := cli.ExtractStackArchive(ctx, "app", "assets.tar.gz", "public"); err != nil {
		t.Fatalf("ExtractStackArchive failed: %v", err)
	}
	lastCmd := executedCmds[len(executedCmds)-1]
	if !strings.Contains(lastCmd, "tar -xf") || !strings.Contains(lastCmd, ".docker-orkes/stacks/app/public") {
		t.Errorf("unexpected tar extract command: %s", lastCmd)
	}

	if err := cli.ExtractStackArchive(ctx, "app", "bundle.zip", ""); err != nil {
		t.Fatalf("ExtractStackArchive zip failed: %v", err)
	}
	lastCmd = executedCmds[len(executedCmds)-1]
	if !strings.Contains(lastCmd, "unzip") {
		t.Errorf("unexpected zip extract command: %s", lastCmd)
	}

	// 4. UploadStackFiles
	files := map[string][]byte{
		"nginx/default.conf": []byte("server {}"),
		"static/index.html":  []byte("<h1>Hi</h1>"),
	}
	if err := cli.UploadStackFiles(ctx, "app", files); err != nil {
		t.Fatalf("UploadStackFiles failed: %v", err)
	}
	uploaded := mock.uploaded[".docker-orkes/stacks/app"]
	if uploaded == nil || string(uploaded["nginx/default.conf"]) != "server {}" {
		t.Errorf("expected uploaded files in mock: %+v", uploaded)
	}

	// 5. Path traversal checks
	if err := cli.CreateStackDir(ctx, "app", "../etc"); err == nil {
		t.Errorf("expected error on path traversal in CreateStackDir")
	}
	if err := cli.DeleteStackPath(ctx, "app", "../../etc/shadow"); err == nil {
		t.Errorf("expected error on path traversal in DeleteStackPath")
	}
}

func TestContainerInfo_MatchesStack(t *testing.T) {
	// 1. With explicit StackName from Compose project label (primary mechanism)
	c1 := ContainerInfo{Names: "my-web-1", StackName: "web"}
	if !c1.MatchesStack("web") {
		t.Errorf("expected c1 to match 'web'")
	}
	if c1.MatchesStack("web-proxy") {
		t.Errorf("expected c1 not to match 'web-proxy'")
	}

	c3 := ContainerInfo{Names: "web-proxy-gateway-1", StackName: "web-proxy"}
	if c3.MatchesStack("web") {
		t.Errorf("c3 is web-proxy, should NOT match 'web'")
	}
	if !c3.MatchesStack("web-proxy") {
		t.Errorf("c3 should match 'web-proxy'")
	}

	// 2. Fallback when label is absent (pure naming prefix matching)
	c2 := ContainerInfo{Names: "web-app-1"}
	if !c2.MatchesStack("web") {
		t.Errorf("expected c2 to match 'web'")
	}
	if c2.MatchesStack("app") {
		t.Errorf("expected c2 not to match 'app'")
	}

	c4 := ContainerInfo{Names: "other-web-1"}
	if c4.MatchesStack("web") {
		t.Errorf("c4 should not match 'web'")
	}
}

func TestUpStackCommand(t *testing.T) {
	mock := &mockExecutor{}
	c := New(Config{
		Host:     "127.0.0.1",
		Port:     22,
		User:     "root",
		Executor: mock,
	})

	_, err := c.UpStack(context.Background(), "my-stack")
	if err != nil {
		t.Fatalf("UpStack failed: %v", err)
	}

	if len(mock.commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(mock.commands))
	}
	cmd := mock.commands[0]
	if !strings.Contains(cmd, "if [ -f") || !strings.Contains(cmd, "--env-file") || !strings.Contains(cmd, "docker compose") {
		t.Errorf("UpStack did not include safe env-file check: %s", cmd)
	}
}
