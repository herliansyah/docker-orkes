package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"docker-orkes/internal/store"
)

func TestParseDockerVolumesOutput(t *testing.T) {
	volRaw := `{"Driver":"local","Labels":"com.docker.compose.project=myapp,com.docker.compose.version=2.0","Name":"myapp_db_data","Scope":"local","Size":"N/A"}
{"Driver":"local","Labels":"","Name":"orphan_volume","Scope":"local","Size":"N/A"}`

	inspectRaw := `[
		{
			"Name": "/myapp-db-1",
			"Mounts": [
				{"Type": "volume", "Name": "myapp_db_data"}
			]
		}
	]`

	vols := parseDockerVolumesOutput(volRaw, inspectRaw)
	if len(vols) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(vols))
	}

	// First volume should be managed and in use
	if !vols[0].IsManaged || vols[0].StackName != "myapp" {
		t.Errorf("expected myapp_db_data to be managed by myapp, got isManaged=%v, stack=%s", vols[0].IsManaged, vols[0].StackName)
	}
	if !vols[0].InUse || len(vols[0].Containers) != 1 || vols[0].Containers[0] != "myapp-db-1" {
		t.Errorf("expected myapp_db_data to be in-use by myapp-db-1, got inUse=%v, containers=%v", vols[0].InUse, vols[0].Containers)
	}

	// Second volume should be unmanaged and unused
	if vols[1].IsManaged {
		t.Errorf("expected orphan_volume to be unmanaged")
	}
	if vols[1].InUse {
		t.Errorf("expected orphan_volume to be unused")
	}
}

func TestParseDockerNetworksOutput(t *testing.T) {
	netRaw := `{"Driver":"bridge","ID":"bridge123","Labels":"","Name":"bridge","Scope":"local"}
{"Driver":"bridge","ID":"net456","Labels":"com.docker.compose.project=webapp","Name":"webapp_default","Scope":"local"}
{"Driver":"bridge","ID":"net789","Labels":"","Name":"custom_net","Scope":"local"}`

	inspectRaw := `[
		{
			"Name": "/webapp-web-1",
			"NetworkSettings": {
				"Networks": {
					"webapp_default": {"NetworkID": "net456"}
				}
			}
		}
	]`

	nets := parseDockerNetworksOutput(netRaw, inspectRaw)
	if len(nets) != 3 {
		t.Fatalf("expected 3 networks, got %d", len(nets))
	}

	// Bridge should be system
	if !nets[0].IsSystem || !nets[0].InUse {
		t.Errorf("expected bridge to be system and in-use")
	}

	// webapp_default should be managed and in-use
	if !nets[1].IsManaged || nets[1].StackName != "webapp" || !nets[1].InUse {
		t.Errorf("expected webapp_default to be managed and in-use")
	}

	// custom_net should be unmanaged and unused
	if nets[2].IsManaged || nets[2].IsSystem || nets[2].InUse {
		t.Errorf("expected custom_net to be unmanaged, non-system, and unused")
	}
}

func TestSystemNetworkDeletionBlocked(t *testing.T) {
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	defer st.Close()

	n := store.Node{
		ID:        "node-1",
		Name:      "test-node",
		Host:      "127.0.0.1",
		Port:      22,
		User:      "root",
		CreatedAt: time.Now(),
	}
	if err := st.CreateNode(n); err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	srv := &Server{
		store: st,
	}

	reqBody := `{"id":"bridge","name":"bridge"}`
	req := httptest.NewRequest("POST", "/nodes/"+n.ID+"/networks/delete", strings.NewReader(reqBody))
	req.SetPathValue("id", n.ID)
	rec := httptest.NewRecorder()

	srv.handleDeleteNodeNetwork(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for system network deletion, got %d: %s", rec.Code, rec.Body.String())
	}
}
