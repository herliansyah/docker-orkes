package gitsync

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Syncer interface {
	Sync(ctx context.Context, mirrorDir string, repoURL string, branch string, privKey string, files map[string][]byte) error
}

type NativeGitSyncer struct{}

func NewSyncer() Syncer {
	return &NativeGitSyncer{}
}

// ponytail: native os/exec git client avoids heavy CGO/pure-Go Git deps while fully utilizing host SSH agent and native Git capabilities.
func (s *NativeGitSyncer) Sync(ctx context.Context, mirrorDir string, repoURL string, branch string, privKey string, files map[string][]byte) error {
	if branch == "" {
		branch = "main"
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name: %s", branch)
	}

	if err := os.MkdirAll(mirrorDir, 0755); err != nil {
		return fmt.Errorf("failed to create git mirror dir: %w", err)
	}

	// Write all files
	for relPath, content := range files {
		fullPath := filepath.Join(mirrorDir, relPath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return fmt.Errorf("failed to create dir for %s: %w", relPath, err)
		}
		if err := os.WriteFile(fullPath, content, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", relPath, err)
		}
	}

	// Initialize git repo if needed
	gitDir := filepath.Join(mirrorDir, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		cmd := exec.CommandContext(ctx, "git", "init", "-b", branch)
		cmd.Dir = mirrorDir
		if out, err := cmd.CombinedOutput(); err != nil {
			// fallback for older git without -b
			cmdInit := exec.CommandContext(ctx, "git", "init")
			cmdInit.Dir = mirrorDir
			if outInit, errInit := cmdInit.CombinedOutput(); errInit != nil {
				return fmt.Errorf("git init failed: %w (out: %s)", errInit, string(outInit))
			}
			_ = string(out)
		}
	}

	// Configure local user
	cfgName := exec.CommandContext(ctx, "git", "config", "user.name", "Docker Orkes")
	cfgName.Dir = mirrorDir
	_ = cfgName.Run()

	cfgEmail := exec.CommandContext(ctx, "git", "config", "user.email", "orkes@localhost")
	cfgEmail.Dir = mirrorDir
	_ = cfgEmail.Run()

	// Stage all files
	cmdAdd := exec.CommandContext(ctx, "git", "add", "-A")
	cmdAdd.Dir = mirrorDir
	if out, err := cmdAdd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add failed: %w (out: %s)", err, string(out))
	}

	// Check status
	cmdStatus := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmdStatus.Dir = mirrorDir
	statusOut, _ := cmdStatus.Output()

	if len(strings.TrimSpace(string(statusOut))) > 0 {
		commitMsg := fmt.Sprintf("chore(backup): sync control plane state [%s]", time.Now().UTC().Format(time.RFC3339))
		cmdCommit := exec.CommandContext(ctx, "git", "commit", "-m", commitMsg)
		cmdCommit.Dir = mirrorDir
		if out, err := cmdCommit.CombinedOutput(); err != nil {
			return fmt.Errorf("git commit failed: %w (out: %s)", err, string(out))
		}
	}

	// If remote repo URL is provided, configure origin and push
	if repoURL != "" {
		if strings.HasPrefix(repoURL, "-") {
			return fmt.Errorf("invalid repository url: %s", repoURL)
		}
		// Set remote
		remotesCmd := exec.CommandContext(ctx, "git", "remote")
		remotesCmd.Dir = mirrorDir
		remotesOut, _ := remotesCmd.Output()

		if strings.Contains(string(remotesOut), "origin") {
			cmdSetURL := exec.CommandContext(ctx, "git", "remote", "set-url", "origin", "--", repoURL)
			cmdSetURL.Dir = mirrorDir
			_ = cmdSetURL.Run()
		} else {
			cmdAddRemote := exec.CommandContext(ctx, "git", "remote", "add", "origin", "--", repoURL)
			cmdAddRemote.Dir = mirrorDir
			_ = cmdAddRemote.Run()
		}
		// Write SSH key to temporary file for GIT_SSH_COMMAND if key is provided
		var env []string
		if privKey != "" {
			keyFile, err := os.CreateTemp("", "orkes-git-key-*")
			if err == nil {
				_, _ = keyFile.WriteString(privKey)
				_ = keyFile.Chmod(0600)
				_ = keyFile.Close()
				defer os.Remove(keyFile.Name())

				sshCmd := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no", keyFile.Name())
				env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCmd)
			}
		}

		// Check if remote branch already exists
		cmdLsRemote := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "origin", branch)
		cmdLsRemote.Dir = mirrorDir
		if len(env) > 0 {
			cmdLsRemote.Env = env
		}
		lsOut, _ := cmdLsRemote.Output()

		if len(strings.TrimSpace(string(lsOut))) > 0 {
			// Remote branch exists; pull and rebase to reconcile any upstream commits
			cmdPull := exec.CommandContext(ctx, "git", "pull", "--rebase", "-X", "theirs", "origin", branch, "--allow-unrelated-histories")
			cmdPull.Dir = mirrorDir
			if len(env) > 0 {
				cmdPull.Env = env
			}
			if out, err := cmdPull.CombinedOutput(); err != nil {
				cmdAbort := exec.CommandContext(ctx, "git", "rebase", "--abort")
				cmdAbort.Dir = mirrorDir
				_ = cmdAbort.Run()
				return fmt.Errorf("git pull --rebase failed: %w (out: %s)", err, string(out))
			}
		}

		cmdPush := exec.CommandContext(ctx, "git", "push", "-u", "origin", "--", branch)
		cmdPush.Dir = mirrorDir
		if len(env) > 0 {
			cmdPush.Env = env
		}
		if out, err := cmdPush.CombinedOutput(); err != nil {
			return fmt.Errorf("git push failed: %w (out: %s)", err, string(out))
		}
	}

	return nil
}
