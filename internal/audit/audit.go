// Package audit provides structured security event logging for auth-vpn.
// All sensitive operations (auth success/failure, admin API calls, disconnects)
// are written to the audit log as JSON lines alongside the regular server log.
package audit

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

const DefaultAuditLog = "/var/log/auth-vpn-audit.log"

// EventType classifies the kind of audit event.
type EventType string

const (
	EventAuthOK       EventType = "auth_ok"
	EventAuthFail     EventType = "auth_fail"
	EventDisconnect   EventType = "disconnect"
	EventAdminAPI     EventType = "admin_api"
	EventProxyDial    EventType = "proxy_dial"
	EventTokenCreated EventType = "token_created"
	EventTokenRevoked EventType = "token_revoked"
	EventBanned       EventType = "banned"
)

// Event is a single audit record.
type Event struct {
	Time      time.Time  `json:"time"`
	Type      EventType  `json:"type"`
	RemoteIP  string     `json:"remote_ip,omitempty"`
	ClientName string    `json:"client_name,omitempty"`
	Method    string     `json:"method,omitempty"` // HTTP method for admin_api events
	Path      string     `json:"path,omitempty"`   // URL path for admin_api events
	Target    string     `json:"target,omitempty"` // dial target for proxy_dial events
	TokenName string     `json:"token_name,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

// Logger writes audit events as JSON lines to a file.
type Logger struct {
	mu  sync.Mutex
	enc *json.Encoder
	f   *os.File
}

var (
	global   *Logger
	globalMu sync.RWMutex
)

// Init opens path for append-only audit logging and sets it as the global
// logger. Safe to call multiple times; subsequent calls replace the logger.
// If path is empty the audit logger writes to stderr only.
func Init(path string) error {
	l, err := New(path)
	if err != nil {
		return err
	}
	globalMu.Lock()
	if global != nil && global.f != nil {
		global.f.Close()
	}
	global = l
	globalMu.Unlock()
	return nil
}

// New creates a Logger that appends to path.
func New(path string) (*Logger, error) {
	if path == "" {
		return &Logger{enc: json.NewEncoder(os.Stderr)}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Logger{enc: json.NewEncoder(f), f: f}, nil
}

// Log writes an event to the logger.
func (l *Logger) Log(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.enc.Encode(e); err != nil {
		log.Printf("audit: write failed: %v", err)
	}
}

// Close closes the underlying file if any.
func (l *Logger) Close() error {
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

// Log writes e to the global audit logger if one is initialised.
// Falls back to the standard logger if no global is set.
func Log(e Event) {
	globalMu.RLock()
	l := global
	globalMu.RUnlock()
	if l == nil {
		// No audit logger configured — emit a minimal structured line via the
		// standard logger so events are never silently dropped.
		data, _ := json.Marshal(e)
		log.Printf("audit: %s", data)
		return
	}
	l.Log(e)
}
