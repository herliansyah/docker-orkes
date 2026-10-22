package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractPidFile(t *testing.T) {
	if f := extractPidFile([]string{}); f != defaultPidFile {
		t.Errorf("expected default %q, got %q", defaultPidFile, f)
	}
	if f := extractPidFile([]string{"-pid", "custom.pid"}); f != "custom.pid" {
		t.Errorf("expected 'custom.pid', got %q", f)
	}
	if f := extractPidFile([]string{"--pid=custom2.pid"}); f != "custom2.pid" {
		t.Errorf("expected 'custom2.pid', got %q", f)
	}
}

func TestPidFileReadWrite(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test.pid")

	currentPid := os.Getpid()
	if err := writePidFile(pidPath, currentPid); err != nil {
		t.Fatalf("writePidFile failed: %v", err)
	}

	readPid, err := readPidFile(pidPath)
	if err != nil {
		t.Fatalf("readPidFile failed: %v", err)
	}
	if readPid != currentPid {
		t.Errorf("expected PID %d, got %d", currentPid, readPid)
	}

	if !isProcessAlive(currentPid) {
		t.Errorf("expected current process %d to be alive", currentPid)
	}

	if isProcessAlive(-999) {
		t.Errorf("expected negative pid to be reported as dead")
	}

	if err := removePidFile(pidPath); err != nil {
		t.Fatalf("removePidFile failed: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("expected pid file to be removed")
	}
}
