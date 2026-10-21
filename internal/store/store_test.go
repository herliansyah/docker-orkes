package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"docker-orkes/internal/store"
)

func TestNodeImageScans(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	node := store.Node{
		ID:        "node-scan-1",
		Name:      "Scan Test Node",
		Host:      "10.0.0.1",
		Port:      22,
		User:      "root",
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateNode(node); err != nil {
		t.Fatalf("failed to create node: %v", err)
	}

	// 1. Initial lookup should return empty map
	scans, err := st.ListNodeImageScans(node.ID)
	if err != nil {
		t.Fatalf("unexpected error listing scans: %v", err)
	}
	if len(scans) != 0 {
		t.Fatalf("expected 0 scans, got %d", len(scans))
	}

	// 2. Save scan
	scan := &store.NodeImageScan{
		NodeID:        node.ID,
		ImageRef:      "nginx:alpine",
		ScannedAt:     time.Now().UTC().Truncate(time.Second),
		Status:        "vulnerable",
		CriticalCount: 1,
		HighCount:     2,
		MediumCount:   3,
		LowCount:      4,
		ReportJSON:    `{"mock":"report"}`,
	}
	if err := st.SaveNodeImageScan(scan); err != nil {
		t.Fatalf("failed to save scan: %v", err)
	}

	// 3. Get single scan
	got, err := st.GetNodeImageScan(node.ID, "nginx:alpine")
	if err != nil {
		t.Fatalf("failed to get scan: %v", err)
	}
	if got.CriticalCount != 1 || got.HighCount != 2 || got.Status != "vulnerable" {
		t.Fatalf("scan data mismatch: %+v", got)
	}
	if got.ReportJSON != `{"mock":"report"}` {
		t.Fatalf("report json mismatch: %s", got.ReportJSON)
	}

	// 4. Update scan (UPSERT check)
	scan.Status = "clean"
	scan.CriticalCount = 0
	scan.HighCount = 0
	if err := st.SaveNodeImageScan(scan); err != nil {
		t.Fatalf("failed to update scan: %v", err)
	}
	gotUpdated, err := st.GetNodeImageScan(node.ID, "nginx:alpine")
	if err != nil {
		t.Fatalf("failed to get updated scan: %v", err)
	}
	if gotUpdated.Status != "clean" || gotUpdated.CriticalCount != 0 {
		t.Fatalf("updated scan mismatch: %+v", gotUpdated)
	}

	// 5. List scans
	all, err := st.ListNodeImageScans(node.ID)
	if err != nil {
		t.Fatalf("failed to list scans: %v", err)
	}
	if len(all) != 1 || all["nginx:alpine"] == nil {
		t.Fatalf("expected 1 scan for nginx:alpine, got %+v", all)
	}
}
