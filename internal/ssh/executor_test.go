package ssh

import (
	"context"
	"strings"
	"testing"
)

func TestExecutor_BootstrapKey_InvalidKeyRejection(t *testing.T) {
	exec := NewExecutor()

	// Malicious key attempting shell command injection
	maliciousKey := `ssh-ed25519 AAAAC3' ; rm -rf / ; echo ' user@evil`
	err := exec.BootstrapKey(context.Background(), "127.0.0.1", 22, "root", "secret", maliciousKey)
	if err == nil {
		t.Fatalf("expected error on malicious public key, got nil")
	}
	if !strings.Contains(err.Error(), "invalid ssh public key") {
		t.Errorf("expected 'invalid ssh public key' error, got %v", err)
	}

	// Completely bogus key
	err = exec.BootstrapKey(context.Background(), "127.0.0.1", 22, "root", "secret", "not-a-key")
	if err == nil {
		t.Fatalf("expected error on invalid key string, got nil")
	}
	if !strings.Contains(err.Error(), "invalid ssh public key") {
		t.Errorf("expected 'invalid ssh public key' error, got %v", err)
	}
}

func TestExecutor_RunCommand_CancelledContext(t *testing.T) {
	exec := NewExecutor()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	_, err := exec.RunCommand(ctx, "127.0.0.1", 2222, "root", "dummy", "echo test")
	if err == nil {
		t.Fatal("expected error on pre-cancelled context, got nil")
	}
}

func TestExecutor_IPv6AddressFormatting(t *testing.T) {
	exec := NewExecutor()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel context

	// Testing IPv6 host should produce address with brackets [2001:db8::1]:2222 and NOT fail with 'too many colons'
	_, err := exec.RunCommand(ctx, "2001:db8::1", 2222, "root", "dummy", "echo test")
	if err == nil {
		t.Fatal("expected error on pre-cancelled context, got nil")
	}
	if strings.Contains(err.Error(), "too many colons") {
		t.Errorf("IPv6 host parsing failed with 'too many colons': %v", err)
	}
}
