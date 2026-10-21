package gitsync_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"docker-orkes/internal/gitsync"
)

func TestNativeGitSyncer_RemoteHasExistingCommits(t *testing.T) {
	// 1. Setup a remote repository with an initial commit (e.g. README created by GitHub)
	remoteDir := t.TempDir()
	initCmd := exec.Command("git", "init", "-b", "main")
	initCmd.Dir = remoteDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init remote repo: %v (%s)", err, out)
	}

	cfgName := exec.Command("git", "config", "user.name", "Remote Test")
	cfgName.Dir = remoteDir
	_ = cfgName.Run()
	cfgEmail := exec.Command("git", "config", "user.email", "remote@test.local")
	cfgEmail.Dir = remoteDir
	_ = cfgEmail.Run()

	cfgDeny := exec.Command("git", "config", "receive.denyCurrentBranch", "updateInstead")
	cfgDeny.Dir = remoteDir
	_ = cfgDeny.Run()

	if err := os.WriteFile(filepath.Join(remoteDir, "README.md"), []byte("# Remote Repo\n"), 0644); err != nil {
		t.Fatalf("failed to write file in remote: %v", err)
	}

	addCmd := exec.Command("git", "add", "README.md")
	addCmd.Dir = remoteDir
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git add in remote: %v (%s)", err, out)
	}

	commitCmd := exec.Command("git", "commit", "-m", "initial remote commit")
	commitCmd.Dir = remoteDir
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to git commit in remote: %v (%s)", err, out)
	}

	// 2. Setup local mirror dir
	localDir := t.TempDir()
	syncer := gitsync.NewSyncer()

	files := map[string][]byte{
		"inventory.json": []byte(`{"nodes":[],"stacks":[]}`),
	}

	// 3. First sync against remoteDir - should reconcile with remote and push cleanly
	if err := syncer.Sync(context.Background(), localDir, remoteDir, "main", "", files); err != nil {
		t.Fatalf("Sync failed when remote has existing commits: %v", err)
	}

	// Verify README.md from remote was retained in local mirror
	readmeBytes, err := os.ReadFile(filepath.Join(localDir, "README.md"))
	if err != nil {
		t.Fatalf("README.md from remote not found in local mirror after sync: %v", err)
	}
	if string(readmeBytes) != "# Remote Repo\n" {
		t.Fatalf("unexpected README.md content: %s", string(readmeBytes))
	}

	// Verify inventory.json is present
	invBytes, err := os.ReadFile(filepath.Join(localDir, "inventory.json"))
	if err != nil {
		t.Fatalf("inventory.json not found in local mirror: %v", err)
	}
	if string(invBytes) != `{"nodes":[],"stacks":[]}` {
		t.Fatalf("unexpected inventory.json content: %s", string(invBytes))
	}

	// 4. Consecutive sync with an update should succeed cleanly
	files["inventory.json"] = []byte(`{"nodes":["node-1"],"stacks":[]}`)
	if err := syncer.Sync(context.Background(), localDir, remoteDir, "main", "", files); err != nil {
		t.Fatalf("consecutive sync failed: %v", err)
	}

	// 5. Consecutive sync with NO changes should succeed cleanly
	if err := syncer.Sync(context.Background(), localDir, remoteDir, "main", "", files); err != nil {
		t.Fatalf("consecutive sync with no changes failed: %v", err)
	}
}

func TestNativeGitSyncer_EmptyRemote(t *testing.T) {
	// Setup a bare remote repository (brand new repo on GitHub/GitLab)
	remoteDir := t.TempDir()
	initCmd := exec.Command("git", "init", "--bare")
	initCmd.Dir = remoteDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to init bare remote repo: %v (%s)", err, out)
	}

	localDir := t.TempDir()
	syncer := gitsync.NewSyncer()

	files := map[string][]byte{
		"inventory.json": []byte(`{"nodes":[],"stacks":[]}`),
	}

	if err := syncer.Sync(context.Background(), localDir, remoteDir, "main", "", files); err != nil {
		t.Fatalf("Sync failed to empty remote: %v", err)
	}
}
