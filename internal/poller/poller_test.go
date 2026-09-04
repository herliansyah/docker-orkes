package poller_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"docker-orkes/internal/controlplane"
	"docker-orkes/internal/drift"
	"docker-orkes/internal/nodeclient"
	"docker-orkes/internal/poller"
	"docker-orkes/internal/ssh"
	"docker-orkes/internal/store"
)

type mockNotifier struct {
	mu     sync.Mutex
	alerts []struct {
		Title      string
		Message    string
		IsRecovery bool
	}
}

func (m *mockNotifier) SendAlert(ctx context.Context, title string, message string, isRecovery bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = append(m.alerts, struct {
		Title      string
		Message    string
		IsRecovery bool
	}{Title: title, Message: message, IsRecovery: isRecovery})
	return nil
}

type fakeEngine struct {
	st               *store.Store
	nodeOnline       map[string]bool
	nodeContainers   map[string][]nodeclient.ContainerInfo
	testNodeCalls    int
}

func (f *fakeEngine) Store() *store.Store {
	return f.st
}

func (f *fakeEngine) TestNode(ctx context.Context, nodeID string) (string, error) {
	f.testNodeCalls++
	if f.nodeOnline[nodeID] {
		return "27.1.1", nil
	}
	return "", errors.New("connection refused")
}

func (f *fakeEngine) BootstrapNode(ctx context.Context, nodeID string, password string, pubKey string) error {
	return nil
}

func (f *fakeEngine) GetNodeTelemetry(ctx context.Context, nodeID string) (string, []nodeclient.ContainerInfo, error) {
	return "20%", f.nodeContainers[nodeID], nil
}

func (f *fakeEngine) GetContainerLogs(ctx context.Context, nodeID string, containerID string) (string, error) {
	return "", nil
}

func (f *fakeEngine) Deploy(ctx context.Context, deploymentID string) error {
	return nil
}

func (f *fakeEngine) Rollback(ctx context.Context, deploymentID string, revisionID string) error {
	return nil
}

func (f *fakeEngine) ListRevisions(ctx context.Context, deploymentID string) ([]store.DeploymentRevision, error) {
	return f.st.ListDeploymentRevisions(deploymentID)
}

func (f *fakeEngine) Restart(ctx context.Context, deploymentID string) error {
	return nil
}

func (f *fakeEngine) Down(ctx context.Context, deploymentID string) error {
	return nil
}

func (f *fakeEngine) CheckDrift(ctx context.Context, deploymentID string) (*drift.DriftResult, error) {
	return nil, nil
}

func (f *fakeEngine) ReconcileDrift(ctx context.Context, deploymentID string) error {
	return nil
}

func (f *fakeEngine) AdoptDrift(ctx context.Context, deploymentID string) error {
	return nil
}

func (f *fakeEngine) ListDeploymentFiles(ctx context.Context, deploymentID string) (string, []ssh.RemoteFileEntry, error) {
	return "", nil, nil
}

func (f *fakeEngine) ViewDeploymentFile(ctx context.Context, deploymentID string, filename string) (string, []byte, error) {
	return "", nil, nil
}

func (f *fakeEngine) SaveDeploymentFile(ctx context.Context, deploymentID string, filename string, content []byte, redeploy bool) (*controlplane.SaveFileResult, error) {
	return nil, nil
}

func (f *fakeEngine) SyncGitMirror(ctx context.Context) error {
	return nil
}

func (f *fakeEngine) BuildGitMirrorFiles() (map[string][]byte, error) {
	return nil, nil
}

func TestPoller_NodeEdgeAlerting(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer st.Close()

	_ = st.CreateNode(store.Node{ID: "n1", Name: "alpha", Host: "1.2.3.4", Port: 22, User: "root", Status: "online"})

	mockNotif := &mockNotifier{}
	fakeEng := &fakeEngine{
		st:             st,
		nodeOnline:     map[string]bool{"n1": true},
		nodeContainers: make(map[string][]nodeclient.ContainerInfo),
	}

	p := poller.New(poller.Config{
		Engine:   fakeEng,
		Notifier: mockNotif,
		Interval: 1 * time.Second,
	})

	ctx := context.Background()

	// 1. Initial poll: node is online, previous state was online -> no alerts
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	if len(mockNotif.alerts) != 0 {
		t.Fatalf("expected 0 alerts initially, got %d", len(mockNotif.alerts))
	}

	// 2. Node goes offline -> 1 alert (edge triggered)
	fakeEng.nodeOnline["n1"] = false
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if len(mockNotif.alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(mockNotif.alerts))
	}
	if mockNotif.alerts[0].IsRecovery != false || mockNotif.alerts[0].Title != "Node alpha is Offline" {
		t.Errorf("unexpected alert: %+v", mockNotif.alerts[0])
	}

	// 3. Node remains offline on next poll -> NO duplicate alert!
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("poll 3: %v", err)
	}
	if len(mockNotif.alerts) != 1 {
		t.Fatalf("expected still 1 alert (no duplicate), got %d", len(mockNotif.alerts))
	}

	// 4. Node comes back online -> recovery alert!
	fakeEng.nodeOnline["n1"] = true
	if err := p.PollOnce(ctx); err != nil {
		t.Fatalf("poll 4: %v", err)
	}
	if len(mockNotif.alerts) != 2 {
		t.Fatalf("expected 2 alerts (including recovery), got %d", len(mockNotif.alerts))
	}
	if mockNotif.alerts[1].IsRecovery != true || mockNotif.alerts[1].Title != "Node alpha Recovered" {
		t.Errorf("unexpected recovery alert: %+v", mockNotif.alerts[1])
	}
}

func TestPoller_ContainerDegradedAndRecovery(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer st.Close()

	_ = st.CreateNode(store.Node{ID: "n1", Name: "alpha", Host: "1.2.3.4", Port: 22, User: "root", Status: "online"})
	_ = st.CreateStack(store.Stack{ID: "s1", Name: "web"})
	_ = st.CreateDeployment(store.Deployment{ID: "d1", NodeID: "n1", StackID: "s1", Status: "running"})

	mockNotif := &mockNotifier{}
	fakeEng := &fakeEngine{
		st:         st,
		nodeOnline: map[string]bool{"n1": true},
		nodeContainers: map[string][]nodeclient.ContainerInfo{
			"n1": {
				{ID: "c1", Names: "web_app_1", State: "running"},
			},
		},
	}

	p := poller.New(poller.Config{
		Engine:   fakeEng,
		Notifier: mockNotif,
	})

	ctx := context.Background()

	// Initial poll: all healthy
	_ = p.PollOnce(ctx)
	if len(mockNotif.alerts) != 0 {
		t.Fatalf("expected 0 alerts, got %d", len(mockNotif.alerts))
	}

	// Container crashes/exits
	fakeEng.nodeContainers["n1"] = []nodeclient.ContainerInfo{
		{ID: "c1", Names: "web_app_1", State: "exited"},
	}
	_ = p.PollOnce(ctx)

	if len(mockNotif.alerts) != 1 {
		t.Fatalf("expected 1 degraded alert, got %d", len(mockNotif.alerts))
	}
	if mockNotif.alerts[0].IsRecovery != false {
		t.Errorf("expected degraded alert")
	}

	// Container restarts/healthy
	fakeEng.nodeContainers["n1"] = []nodeclient.ContainerInfo{
		{ID: "c1", Names: "web_app_1", State: "running"},
	}
	_ = p.PollOnce(ctx)

	if len(mockNotif.alerts) != 2 {
		t.Fatalf("expected 2 alerts (including recovery), got %d", len(mockNotif.alerts))
	}
	if mockNotif.alerts[1].IsRecovery != true {
		t.Errorf("expected recovery alert, got: %+v", mockNotif.alerts[1])
	}
}

func TestPoller_DisabledSetting(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer st.Close()

	_ = st.CreateNode(store.Node{ID: "n1", Name: "alpha", Host: "1.2.3.4", Port: 22, User: "root", Status: "online"})
	_ = st.SetSetting("poller_enabled", "false")

	mockNotif := &mockNotifier{}
	fakeEng := &fakeEngine{
		st:         st,
		nodeOnline: map[string]bool{"n1": false}, // offline!
	}

	p := poller.New(poller.Config{
		Engine:   fakeEng,
		Notifier: mockNotif,
	})

	_ = p.PollOnce(context.Background())
	if fakeEng.testNodeCalls != 0 {
		t.Errorf("expected 0 node test calls when disabled, got %d", fakeEng.testNodeCalls)
	}
	if len(mockNotif.alerts) != 0 {
		t.Errorf("expected 0 alerts when disabled, got %d", len(mockNotif.alerts))
	}
}

func TestPoller_StartStop(t *testing.T) {
	st, _ := store.New(":memory:")
	defer st.Close()

	p := poller.New(poller.Config{
		Engine:   &fakeEngine{st: st, nodeOnline: map[string]bool{}},
		Notifier: &mockNotifier{},
		Interval: 50 * time.Millisecond,
	})

	p.Start()
	// Calling Start again should be a no-op
	p.Start()

	time.Sleep(120 * time.Millisecond)
	p.Stop()
	// Calling Stop again should be safe
	p.Stop()
}

