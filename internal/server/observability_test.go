package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"docker-orkes/internal/server"
	"docker-orkes/internal/store"
)

type mockObsExecutor struct {
	mockDeployExecutor
	dfOutput           string
	dockerPsOutput     string
	dockerLogOut       string
	dockerImagesOutput string
	dockerPullOutput   map[string]string
	lastRmiCmd         string
	lastPullCmd        string
}

func (m *mockObsExecutor) RunCommand(ctx context.Context, host string, port int, user string, privKey string, cmd string) (string, error) {
	if strings.HasPrefix(cmd, "df") {
		return m.dfOutput, nil
	}
	if strings.Contains(cmd, "docker ps") {
		return m.dockerPsOutput, nil
	}
	if strings.Contains(cmd, "docker logs") {
		return m.dockerLogOut, nil
	}
	if strings.Contains(cmd, "docker images") {
		return m.dockerImagesOutput, nil
	}
	if strings.HasPrefix(cmd, "docker rmi") {
		m.lastRmiCmd = cmd
		return "Deleted: " + strings.TrimPrefix(cmd, "docker rmi "), nil
	}
	if strings.HasPrefix(cmd, "docker pull") {
		m.lastPullCmd = cmd
		ref := strings.TrimSpace(strings.TrimPrefix(cmd, "docker pull"))
		if m.dockerPullOutput != nil && m.dockerPullOutput[ref] != "" {
			return m.dockerPullOutput[ref], nil
		}
		return "Status: Downloaded newer image for " + ref, nil
	}
	return m.mockDeployExecutor.RunCommand(ctx, host, port, user, privKey, cmd)
}

func TestServer_NodeObservabilityAndLogs(t *testing.T) {
	mockExec := &mockObsExecutor{
		dfOutput: "Filesystem      Size  Used Avail Use% Mounted on\n/dev/root        50G   15G   35G  30% /\n",
		dockerPsOutput: `{"ID":"c123","Names":"web-app-1","Image":"nginx:alpine","Status":"Up 2 hours","State":"running","Ports":"0.0.0.0:80->80/tcp"}
{"ID":"c456","Names":"redis-db","Image":"redis:7","Status":"Exited (0) 5 mins ago","State":"exited","Ports":""}
`,
		dockerLogOut: "2026-09-03 10:00:00 [notice] 1#1: start worker processes\n2026-09-03 10:00:01 HTTP 200 GET /\n",
	}

	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
		Executor:       mockExec,
	})
	if err != nil {
		t.Fatalf("failed to initialize server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// 1. Authenticate
	loginForm := url.Values{"password": {"secret-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// 2. Register Node
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-obs",
		Name:   "Telemetry Node",
		Host:   "10.0.0.88",
		Port:   22,
		User:   "root",
		Status: "online",
	})

	// 3. Query GET /nodes/node-obs/telemetry
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-obs/telemetry", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for telemetry, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var telemetry struct {
		Disk struct {
			Total   string `json:"total"`
			Used    string `json:"used"`
			Free    string `json:"free"`
			Percent string `json:"percent"`
		} `json:"disk"`
		Containers []struct {
			ID    string `json:"id"`
			Names string `json:"names"`
			Image string `json:"image"`
			State string `json:"state"`
		} `json:"containers"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &telemetry); err != nil {
		t.Fatalf("failed to unmarshal telemetry json: %v, body: %s", err, rec.Body.String())
	}

	if telemetry.Disk.Total != "50G" || telemetry.Disk.Used != "15G" || telemetry.Disk.Percent != "30%" {
		t.Errorf("unexpected disk metrics: %+v", telemetry.Disk)
	}

	if len(telemetry.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(telemetry.Containers))
	}
	if telemetry.Containers[0].Names != "web-app-1" || telemetry.Containers[0].State != "running" {
		t.Errorf("unexpected container 0: %+v", telemetry.Containers[0])
	}
	if telemetry.Containers[1].Names != "redis-db" || telemetry.Containers[1].State != "exited" {
		t.Errorf("unexpected container 1: %+v", telemetry.Containers[1])
	}

	// 4. Query GET /nodes/node-obs/containers/c123/logs
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-obs/containers/c123/logs", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for logs, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "start worker processes") {
		t.Errorf("expected container logs to contain 'start worker processes', got: %s", rec.Body.String())
	}

	// 5. Query GET /nodes/node-obs HTML page
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-obs", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for node detail view, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Telemetry Node") {
		t.Errorf("expected node detail view to contain node name")
	}
}

func TestServer_NodeImagesObservabilityAndRetention(t *testing.T) {
	mockExec := &mockObsExecutor{
		dockerPsOutput: `{"ID":"c101","Names":"web-app-1","Image":"nginx:alpine","Status":"Up 2 hours","State":"running","Ports":"80/tcp"}
{"ID":"c102","Names":"redis-db","Image":"redis:7","Status":"Exited (0) 5 mins ago","State":"exited","Ports":""}
`,
		dockerImagesOutput: `{"ID":"sha256:img1111","Repository":"nginx","Tag":"alpine","Size":"45MB","CreatedSince":"2 days ago"}
{"ID":"sha256:img2222","Repository":"redis","Tag":"7","Size":"120MB","CreatedSince":"1 week ago"}
{"ID":"sha256:img3333","Repository":"percona/percona-xtrabackup","Tag":"8.0","Size":"250MB","CreatedSince":"3 weeks ago"}
{"ID":"sha256:img4444","Repository":"<none>","Tag":"<none>","Size":"80MB","CreatedSince":"1 month ago"}
`,
	}

	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "secret-password",
		Executor:       mockExec,
	})
	if err != nil {
		t.Fatalf("failed to initialize server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// Login
	loginForm := url.Values{"password": {"secret-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// Create Node
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-img-test",
		Name:   "Image Test Node",
		Host:   "10.0.0.99",
		Port:   22,
		User:   "root",
		Status: "online",
	})

	// 1. GET /nodes/node-img-test/images
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-img-test/images", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for images query, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var images []server.NodeImageInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &images); err != nil {
		t.Fatalf("failed to unmarshal images: %v", err)
	}
	if len(images) != 4 {
		t.Fatalf("expected 4 images, got %d", len(images))
	}

	imgMap := make(map[string]server.NodeImageInfo)
	for _, img := range images {
		key := img.Repository + ":" + img.Tag
		imgMap[key] = img
	}

	// Verify states
	if imgMap["nginx:alpine"].Status != "in_use_running" {
		t.Errorf("expected nginx:alpine to be in_use_running, got %s", imgMap["nginx:alpine"].Status)
	}
	if imgMap["redis:7"].Status != "in_use_stopped" {
		t.Errorf("expected redis:7 to be in_use_stopped, got %s", imgMap["redis:7"].Status)
	}
	if imgMap["percona/percona-xtrabackup:8.0"].Status != "unused" {
		t.Errorf("expected percona to be unused initially, got %s", imgMap["percona/percona-xtrabackup:8.0"].Status)
	}
	if imgMap["<none>:<none>"].Status != "dangling" {
		t.Errorf("expected <none>:<none> to be dangling, got %s", imgMap["<none>:<none>"].Status)
	}

	// 2. PIN percona as On-Demand
	pinBody := `{"image_ref":"percona/percona-xtrabackup:8.0","pinned":true}`
	req = httptest.NewRequest(http.MethodPost, "/nodes/node-img-test/images/pin", strings.NewReader(pinBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for pin, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Query images again, check status changed to on_demand
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-img-test/images", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	_ = json.Unmarshal(rec.Body.Bytes(), &images)
	imgMap = make(map[string]server.NodeImageInfo)
	for _, img := range images {
		imgMap[img.Repository+":"+img.Tag] = img
	}
	if imgMap["percona/percona-xtrabackup:8.0"].Status != "on_demand" || !imgMap["percona/percona-xtrabackup:8.0"].IsPinned {
		t.Errorf("expected percona to be on_demand and is_pinned=true, got status=%s, is_pinned=%v",
			imgMap["percona/percona-xtrabackup:8.0"].Status, imgMap["percona/percona-xtrabackup:8.0"].IsPinned)
	}

	// 3. DELETE guards
	// A: Try to delete in-use image (nginx:alpine) -> must fail (400 Bad Request)
	delNginx := `{"image_ref":"nginx:alpine"}`
	req = httptest.NewRequest(http.MethodPost, "/nodes/node-img-test/images/delete", strings.NewReader(delNginx))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when deleting in-use image, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "in use by container") {
		t.Errorf("expected error message to mention container usage, got: %s", rec.Body.String())
	}

	// B: Try to delete pinned on-demand image -> must fail (400 Bad Request)
	delPercona := `{"image_ref":"percona/percona-xtrabackup:8.0"}`
	req = httptest.NewRequest(http.MethodPost, "/nodes/node-img-test/images/delete", strings.NewReader(delPercona))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when deleting pinned image, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pinned as an On-Demand Image") {
		t.Errorf("expected error message to mention On-Demand Image, got: %s", rec.Body.String())
	}

	// C: Delete dangling image by ID -> must succeed
	delDangling := `{"image_ref":"<none>:<none>","image_id":"sha256:img4444"}`
	req = httptest.NewRequest(http.MethodPost, "/nodes/node-img-test/images/delete", strings.NewReader(delDangling))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for deleting dangling image, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(mockExec.lastRmiCmd, "sha256:img4444") {
		t.Errorf("expected rmi command with image ID, got: %s", mockExec.lastRmiCmd)
	}

	// D: Unpin percona and delete -> must succeed
	unpinBody := `{"image_ref":"percona/percona-xtrabackup:8.0","pinned":false}`
	req = httptest.NewRequest(http.MethodPost, "/nodes/node-img-test/images/pin", strings.NewReader(unpinBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for unpin, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/nodes/node-img-test/images/delete", strings.NewReader(delPercona))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for deleting unpinned percona image, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(mockExec.lastRmiCmd, "percona/percona-xtrabackup:8.0") {
		t.Errorf("expected rmi command with percona ref, got: %s", mockExec.lastRmiCmd)
	}
}

func TestServer_ManagedVsUnmanagedContainersAndImages(t *testing.T) {
	mockExec := &mockObsExecutor{
		dfOutput: "Filesystem      Size  Used Avail Use% Mounted on\n/dev/root        50G   15G   35G  30% /\n",
		dockerPsOutput: `{"ID":"c_managed_1","Names":"web-1","Image":"custom-web:1.0","Status":"Up 1 hour","State":"running","Ports":"80/tcp","Labels":"com.docker.compose.project=my-web-stack,com.docker.compose.project.working_dir=/root/.docker-orkes/stacks/my-web-stack"}
{"ID":"c_unmanaged_2","Names":"host-daemon","Image":"runner:latest","Status":"Up 3 hours","State":"running","Ports":"9000/tcp","Labels":"maintainer=hostadmin"}
`,
		dockerImagesOutput: `{"ID":"sha256:img_web","Repository":"custom-web","Tag":"1.0","Size":"100MB","CreatedSince":"1 day ago"}
{"ID":"sha256:img_runner","Repository":"runner","Tag":"latest","Size":"200MB","CreatedSince":"2 days ago"}
{"ID":"sha256:img_redis","Repository":"redis","Tag":"7","Size":"50MB","CreatedSince":"3 days ago"}
`,
	}

	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "test-password",
		Executor:       mockExec,
	})
	if err != nil {
		t.Fatalf("failed to initialize server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// Login
	loginForm := url.Values{"password": {"test-password"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// Create Node
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-origin-test",
		Name:   "Origin Test Node",
		Host:   "192.168.1.50",
		Port:   22,
		User:   "root",
		Status: "online",
	})

	// Create Stack & Deployment
	_ = srv.Store().CreateStack(store.Stack{
		ID:          "stack-web-1",
		Name:        "my-web-stack",
		ComposeYAML: "services:\n  web:\n    image: custom-web:1.0\n",
	})
	_ = srv.Store().CreateDeployment(store.Deployment{
		ID:        "dep-web-1",
		StackID:   "stack-web-1",
		NodeID:    "node-origin-test",
		StackName: "my-web-stack",
		Status:    "running",
	})

	// 1. Verify GET /nodes/node-origin-test/telemetry
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-origin-test/telemetry", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for telemetry, got %d", rec.Code)
	}

	var telemetry struct {
		Containers []server.ContainerInfo `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &telemetry); err != nil {
		t.Fatalf("failed to parse telemetry json: %v", err)
	}

	if len(telemetry.Containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(telemetry.Containers))
	}

	cMap := make(map[string]server.ContainerInfo)
	for _, c := range telemetry.Containers {
		cMap[c.Names] = c
	}

	managedC := cMap["web-1"]
	if !managedC.IsManaged {
		t.Errorf("expected web-1 to be managed, got is_managed=%v", managedC.IsManaged)
	}
	if managedC.StackName != "my-web-stack" {
		t.Errorf("expected web-1 stack_name='my-web-stack', got '%s'", managedC.StackName)
	}

	unmanagedC := cMap["host-daemon"]
	if unmanagedC.IsManaged {
		t.Errorf("expected host-daemon to be unmanaged, got is_managed=%v", unmanagedC.IsManaged)
	}

	// 2. Verify GET /nodes/node-origin-test/images
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-origin-test/images", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for images, got %d", rec.Code)
	}

	var images []server.NodeImageInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &images); err != nil {
		t.Fatalf("failed to parse images json: %v", err)
	}

	imgMap := make(map[string]server.NodeImageInfo)
	for _, img := range images {
		imgMap[img.Repository+":"+img.Tag] = img
	}

	webImg := imgMap["custom-web:1.0"]
	if !webImg.IsManaged || webImg.UsageOrigin != "managed" {
		t.Errorf("expected custom-web:1.0 to have usage_origin='managed', got is_managed=%v origin='%s'", webImg.IsManaged, webImg.UsageOrigin)
	}

	runnerImg := imgMap["runner:latest"]
	if runnerImg.IsManaged || runnerImg.UsageOrigin != "unmanaged" {
		t.Errorf("expected runner:latest to have usage_origin='unmanaged', got is_managed=%v origin='%s'", runnerImg.IsManaged, runnerImg.UsageOrigin)
	}

	redisImg := imgMap["redis:7"]
	if redisImg.UsageOrigin != "" || redisImg.Status != "unused" {
		t.Errorf("expected redis:7 to be unused with empty origin, got status='%s' origin='%s'", redisImg.Status, redisImg.UsageOrigin)
	}

	// 3. Verify GET /nodes/node-origin-test renders search & filter controls
	req = httptest.NewRequest(http.MethodGet, "/nodes/node-origin-test", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for node detail, got %d", rec.Code)
	}
	html := rec.Body.String()
	for _, marker := range []string{
		"containers-search",
		"containers-origin-filter",
		"containers-state-filter",
		"images-search",
		"images-status-filter",
		"Origin",
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("expected node detail html to contain %q", marker)
		}
	}
}

func TestServer_ImagePullAndDeploymentRollout(t *testing.T) {
	mockExec := &mockObsExecutor{
		dfOutput: "Filesystem      Size  Used Avail Use% Mounted on\n/dev/root        50G   15G   35G  30% /\n",
		dockerPsOutput: `{"ID":"c_api_1","Names":"api-app","Image":"myreg.local:5000/api:latest","Status":"Up 2 hours","State":"running","Ports":"8080/tcp","Labels":"com.docker.compose.project=api-stack,com.docker.compose.project.working_dir=/root/.docker-orkes/stacks/api-stack"}
`,
		dockerImagesOutput: `{"ID":"sha256:img_api_old","Repository":"myreg.local:5000/api","Tag":"latest","Size":"150MB","CreatedSince":"3 days ago"}
{"ID":"sha256:img_redis","Repository":"redis","Tag":"alpine","Size":"30MB","CreatedSince":"5 days ago"}
`,
		dockerPullOutput: map[string]string{
			"myreg.local:5000/api:latest": "Digest: sha256:newdigest12345\nStatus: Downloaded newer image for myreg.local:5000/api:latest",
			"redis:alpine":                "Status: Image is up to date for redis:alpine",
		},
	}

	srv, err := server.New(server.Config{
		DBPath:         ":memory:",
		MasterPassword: "pull-test-pass",
		Executor:       mockExec,
	})
	if err != nil {
		t.Fatalf("failed to initialize server: %v", err)
	}
	defer srv.Close()

	handler := srv.Handler()

	// Login
	loginForm := url.Values{"password": {"pull-test-pass"}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	// Create Node
	_ = srv.Store().CreateNode(store.Node{
		ID:     "node-pull-test",
		Name:   "Pull Test Node",
		Host:   "10.10.1.10",
		Port:   22,
		User:   "root",
		Status: "online",
	})

	// Create Stack & Deployment
	_ = srv.Store().CreateStack(store.Stack{
		ID:          "stack-api",
		Name:        "api-stack",
		ComposeYAML: "services:\n  api:\n    image: myreg.local:5000/api:latest\n",
	})
	_ = srv.Store().CreateDeployment(store.Deployment{
		ID:        "dep-api-1",
		StackID:   "stack-api",
		NodeID:    "node-pull-test",
		StackName: "api-stack",
		Status:    "running",
	})

	// 1. Pull updated image: myreg.local:5000/api:latest
	pullBody1 := `{"image_ref":"myreg.local:5000/api:latest"}`
	req = httptest.NewRequest(http.MethodPost, "/nodes/node-pull-test/images/pull", strings.NewReader(pullBody1))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for pull updated image, got %d (%s)", rec.Code, rec.Body.String())
	}

	var res1 struct {
		Success           bool                `json:"success"`
		ImageRef          string              `json:"image_ref"`
		Status            string              `json:"status"`
		UsedByDeployments []map[string]string `json:"used_by_deployments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res1); err != nil {
		t.Fatalf("failed to unmarshal pull response: %v", err)
	}

	if !res1.Success || res1.Status != "updated" {
		t.Errorf("expected pull status='updated', got '%s'", res1.Status)
	}
	if len(res1.UsedByDeployments) == 0 || res1.UsedByDeployments[0]["stack_name"] != "api-stack" {
		t.Errorf("expected used_by_deployments to contain 'api-stack', got %+v", res1.UsedByDeployments)
	}

	// 2. Pull up-to-date image: redis:alpine
	pullBody2 := `{"image_ref":"redis:alpine"}`
	req = httptest.NewRequest(http.MethodPost, "/nodes/node-pull-test/images/pull", strings.NewReader(pullBody2))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for pull up-to-date image, got %d (%s)", rec.Code, rec.Body.String())
	}

	var res2 struct {
		Success           bool                `json:"success"`
		Status            string              `json:"status"`
		UsedByDeployments []map[string]string `json:"used_by_deployments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res2); err != nil {
		t.Fatalf("failed to unmarshal pull response 2: %v", err)
	}

	if !res2.Success || res2.Status != "up_to_date" {
		t.Errorf("expected pull status='up_to_date', got '%s'", res2.Status)
	}
	if len(res2.UsedByDeployments) != 0 {
		t.Errorf("expected no deployments using redis:alpine, got %+v", res2.UsedByDeployments)
	}

	// 3. Rollout Deployment via POST /deployments/dep-api-1/rollout
	req = httptest.NewRequest(http.MethodPost, "/deployments/dep-api-1/rollout", nil)
	req.Header.Set("Accept", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for rollout, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "successfully") {
		t.Errorf("expected rollout success message, got %s", rec.Body.String())
	}

	// Verify pull command was executed by SSH executor
	if !strings.Contains(mockExec.lastPullCmd, "redis:alpine") {
		t.Errorf("expected lastPullCmd to be redis:alpine, got %s", mockExec.lastPullCmd)
	}
}



