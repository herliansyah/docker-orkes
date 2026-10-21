package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

type KeyPair struct {
	PrivateKeyPEM string
	PublicKeySSH  string
}

// GenerateEd25519KeyPair generates an Ed25519 private key and corresponding OpenSSH public key.
func GenerateEd25519KeyPair() (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 key: %w", err)
	}

	// Marshal private key to OpenSSH PEM format
	privBlock, err := ssh.MarshalPrivateKey(priv, "docker-orkes-control-plane")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}
	privPEM := pem.EncodeToMemory(privBlock)

	// Format public key in authorized_keys format
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ssh public key: %w", err)
	}
	pubSSH := fmt.Sprintf("%s docker-orkes-control-plane\n", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))))

	return &KeyPair{
		PrivateKeyPEM: string(privPEM),
		PublicKeySSH:  pubSSH,
	}, nil
}
