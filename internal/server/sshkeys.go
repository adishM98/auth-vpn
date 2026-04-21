package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHKeyEntry stores a named public key in authorized_keys format.
type SSHKeyEntry struct {
	Name      string    `json:"name"`
	PublicKey string    `json:"public_key"` // authorized_keys format: "ssh-rsa AAAA..."
	CreatedAt time.Time `json:"created_at"`
}

// SSHKeysManager manages SSH public keys for auth-vpn's embedded SSH server.
type SSHKeysManager struct {
	path    string
	mu      sync.RWMutex
	entries []SSHKeyEntry
}

func NewSSHKeysManager(path string) (*SSHKeysManager, error) {
	m := &SSHKeysManager{path: path, entries: []SSHKeyEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil
		}
		return nil, fmt.Errorf("read ssh keys %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &m.entries); err != nil {
		return nil, fmt.Errorf("parse ssh keys: %w", err)
	}
	return m, nil
}

func (m *SSHKeysManager) save() error {
	data, err := json.MarshalIndent(m.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, data, 0o600)
}

// Add registers a new public key. Returns error if name or key already exists.
func (m *SSHKeysManager) Add(name, publicKey string) error {
	// Validate that the public key parses correctly.
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey)); err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, e := range m.entries {
		if e.Name == name {
			return fmt.Errorf("key %q already registered", name)
		}
	}
	m.entries = append(m.entries, SSHKeyEntry{
		Name:      name,
		PublicKey: publicKey,
		CreatedAt: time.Now().UTC(),
	})
	return m.save()
}

// Remove deletes a key by name.
func (m *SSHKeysManager) Remove(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, e := range m.entries {
		if e.Name == name {
			m.entries = append(m.entries[:i], m.entries[i+1:]...)
			return m.save()
		}
	}
	return fmt.Errorf("key %q not found", name)
}

// List returns a copy of all entries.
func (m *SSHKeysManager) List() []SSHKeyEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SSHKeyEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Generate creates an RSA keypair, registers the public key under name, and
// returns the PEM-encoded private key. The private key is never stored — caller
// must deliver it to the end user immediately.
func (m *SSHKeysManager) Generate(name string) (privateKeyPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}

	pub, err := ssh.NewPublicKey(&key.PublicKey)
	if err != nil {
		return "", fmt.Errorf("derive public key: %w", err)
	}
	pubLine := string(ssh.MarshalAuthorizedKey(pub))

	if err := m.Add(name, pubLine); err != nil {
		return "", err
	}

	pemBlock := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return string(pemBlock), nil
}

// FindKey returns the name associated with the given public key, or "" if not found.
func (m *SSHKeysManager) FindKey(key ssh.PublicKey) (string, bool) {
	fp := ssh.FingerprintSHA256(key)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(e.PublicKey))
		if err != nil {
			continue
		}
		if ssh.FingerprintSHA256(parsed) == fp {
			return e.Name, true
		}
	}
	return "", false
}
