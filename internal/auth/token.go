package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// ErrNameTaken is returned by Add when a token with that name already exists.
var ErrNameTaken = errors.New("token name already exists")

type tokensFile struct {
	Tokens []*Token `yaml:"tokens"`
}

// Manager handles token CRUD and validation.
//
// The CLI (`server tokens ...`) and a running server each hold their own
// Manager over the same file, so every operation first re-reads the file if
// another process changed it. Without that, a CLI revoke is ignored by the
// running server and later written back by the server's next save.
type Manager struct {
	mu       sync.Mutex
	tokens   []*Token
	filePath string
	stamp    fileStamp // file state as of our last load/save
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func stampOf(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size()}, nil
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
	m.stamp, _ = stampOf(m.filePath)
	return nil
}

// refresh reloads the file when another process changed it. Caller holds mu.
func (m *Manager) refresh() error {
	st, err := stampOf(m.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if st == m.stamp {
		return nil
	}
	return m.load()
}

func (m *Manager) save() error {
	if err := os.MkdirAll(filepath.Dir(m.filePath), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(tokensFile{Tokens: m.tokens})
	if err != nil {
		return err
	}
	// Write-then-rename so a crash never leaves a truncated tokens file.
	tmp := m.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.filePath); err != nil {
		return err
	}
	m.stamp, _ = stampOf(m.filePath)
	return nil
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
	if err := m.refresh(); err != nil {
		return "", err
	}
	// Names are the revocation handle, so they must be unique.
	for _, t := range m.tokens {
		if t.Name == name {
			return "", fmt.Errorf("%w: %q", ErrNameTaken, name)
		}
	}

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
	if err := m.refresh(); err != nil {
		return nil, fmt.Errorf("reload tokens: %w", err)
	}

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
			// If the use can't be recorded, refuse it: otherwise the token
			// becomes reusable after a restart.
			if err := m.save(); err != nil {
				t.Used = false
				return nil, fmt.Errorf("record one-time token use: %w", err)
			}
		}
		return t, nil
	}
	return nil, fmt.Errorf("invalid token")
}

// Revoke removes every token with the given name (older files may hold
// duplicates from before names were unique).
func (m *Manager) Revoke(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.refresh(); err != nil {
		return err
	}

	kept := m.tokens[:0:0]
	for _, t := range m.tokens {
		if t.Name != name {
			kept = append(kept, t)
		}
	}
	if len(kept) == len(m.tokens) {
		return fmt.Errorf("token %q not found", name)
	}
	m.tokens = kept
	return m.save()
}

// List returns a snapshot of all tokens.
func (m *Manager) List() []*Token {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.refresh() // a stale list is better than none
	out := make([]*Token, len(m.tokens))
	copy(out, m.tokens)
	return out
}
