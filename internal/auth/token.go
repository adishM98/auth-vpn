package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Token represents a stored access token (hash only — raw value never stored).
type Token struct {
	Name      string     `yaml:"name"       json:"name"`
	Hash      string     `yaml:"hash"       json:"-"`
	CreatedAt time.Time  `yaml:"created_at" json:"created_at"`
	ExpiresAt *time.Time `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	OneTime   bool       `yaml:"one_time,omitempty"   json:"one_time,omitempty"`
	Used      bool       `yaml:"used,omitempty"       json:"used,omitempty"`
}

type tokensFile struct {
	Tokens []*Token `yaml:"tokens"`
}

// Manager handles token CRUD and validation.
type Manager struct {
	mu       sync.RWMutex
	tokens   []*Token
	filePath string
}

// NewManager loads tokens from filePath (creates the file if missing).
func NewManager(filePath string) (*Manager, error) {
	m := &Manager{filePath: filePath}
	if err := m.load(); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	data, err := os.ReadFile(m.filePath)
	if err != nil {
		return err
	}
	var tf tokensFile
	if err := yaml.Unmarshal(data, &tf); err != nil {
		return err
	}
	m.tokens = tf.Tokens
	return nil
}

func (m *Manager) save() error {
	if err := os.MkdirAll(filepath.Dir(m.filePath), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(tokensFile{Tokens: m.tokens})
	if err != nil {
		return err
	}
	return os.WriteFile(m.filePath, data, 0o600)
}

// Generate creates a cryptographically random 32-char hex token string.
func Generate() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Add creates a new token entry. Returns the raw (unhashed) token string.
func (m *Manager) Add(name string, expiresAt *time.Time, oneTime bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	raw, err := Generate()
	if err != nil {
		return "", err
	}
	m.tokens = append(m.tokens, &Token{
		Name:      name,
		Hash:      hashToken(raw),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: expiresAt,
		OneTime:   oneTime,
	})
	return raw, m.save()
}

// Validate checks a raw token string and returns the matching Token or an error.
func (m *Manager) Validate(raw string) (*Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	h := hashToken(raw)
	for _, t := range m.tokens {
		if t.Hash != h {
			continue
		}
		if t.OneTime && t.Used {
			return nil, fmt.Errorf("token already used")
		}
		if t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt) {
			return nil, fmt.Errorf("token expired")
		}
		if t.OneTime {
			t.Used = true
			_ = m.save()
		}
		return t, nil
	}
	return nil, fmt.Errorf("invalid token")
}

// Revoke removes the first token with the given name.
func (m *Manager) Revoke(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, t := range m.tokens {
		if t.Name == name {
			m.tokens = append(m.tokens[:i], m.tokens[i+1:]...)
			return m.save()
		}
	}
	return fmt.Errorf("token %q not found", name)
}

// List returns a snapshot of all tokens.
func (m *Manager) List() []*Token {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Token, len(m.tokens))
	copy(out, m.tokens)
	return out
}
