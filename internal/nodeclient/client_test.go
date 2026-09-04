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
