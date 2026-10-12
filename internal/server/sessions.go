package server

import (
	"io"
	"sync"
	"time"
)

// Credential kinds a session can be admitted by. Revoking a credential closes
// only the sessions it admitted, so a token and a whitelist entry that happen
// to share a name don't take each other down.
const (
	viaToken     = "token"
	viaWhitelist = "whitelist"
	viaSSHKey    = "sshkey"    // key registered through /api/ssh-keys
	viaSystemKey = "systemkey" // key from a host authorized_keys file
)

// session is one live authenticated connection: TUN, proxy or SSH.
type session struct {
	name   string
	via    string
	mode   string // "tun", "proxy", "ssh"
	remote string
	since  time.Time
	conn   io.Closer
}

// sessionSet tracks every live session so that Kick and revocation can reach
// all of them, not just TUN clients.
type sessionSet struct {
	mu sync.Mutex
	m  map[*session]struct{}
}

func newSessionSet() *sessionSet { return &sessionSet{m: make(map[*session]struct{})} }

// add registers a session and returns the function that unregisters it.
func (s *sessionSet) add(sess *session) func() {
	sess.since = time.Now().UTC()
	s.mu.Lock()
	s.m[sess] = struct{}{}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.m, sess)
		s.mu.Unlock()
	}
}

// closeWhere closes every session for name admitted by one of vias (any
// credential when vias is empty) and returns how many it closed.
func (s *sessionSet) closeWhere(name string, vias ...string) int {
	s.mu.Lock()
	var hit []io.Closer
	for sess := range s.m {
		if sess.name == name && (len(vias) == 0 || contains(vias, sess.via)) {
			hit = append(hit, sess.conn)
		}
	}
	s.mu.Unlock()
	for _, c := range hit {
		c.Close()
	}
	return len(hit)
}

// nonTUN lists proxy and SSH sessions (TUN clients are listed by the registry,
// which also knows their tunnel IP and byte counters).
func (s *sessionSet) nonTUN() []clientInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []clientInfo
	for sess := range s.m {
		if sess.mode != "tun" {
			out = append(out, clientInfo{Name: sess.name, Mode: sess.mode, ConnectedAt: sess.since})
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
