package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// DirectForward defines one TCP port exposed by the server for whitelisted IPs.
type DirectForward struct {
	ListenPort int       `json:"listen_port"`
	Target     string    `json:"target"`     // e.g. "127.0.0.1:5432"
	CreatedAt  time.Time `json:"created_at"`
}

// ForwardsManager manages direct forward rules with JSON persistence.
type ForwardsManager struct {
	path    string
	mu      sync.RWMutex
	entries []DirectForward
}

// NewForwardsManager loads rules from path. Missing file is not an error.
func NewForwardsManager(path string) (*ForwardsManager, error) {
	fm := &ForwardsManager{path: path, entries: []DirectForward{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fm, nil
		}
		return nil, fmt.Errorf("read forwards %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &fm.entries); err != nil {
		return nil, fmt.Errorf("parse forwards: %w", err)
	}
	return fm, nil
}

func (fm *ForwardsManager) save() error {
	data, err := json.MarshalIndent(fm.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fm.path, data, 0o600)
}

// Add adds a new forward rule. Returns error if port already exists.
func (fm *ForwardsManager) Add(listenPort int, target string) error {
	if listenPort < 1 || listenPort > 65535 {
		return fmt.Errorf("invalid port %d", listenPort)
	}
	// validate target is host:port
	if _, _, err := net.SplitHostPort(target); err != nil {
		return fmt.Errorf("invalid target %q: must be host:port", target)
	}

	fm.mu.Lock()
	defer fm.mu.Unlock()

	for _, e := range fm.entries {
		if e.ListenPort == listenPort {
			return fmt.Errorf("port %d is already forwarded to %s", listenPort, e.Target)
		}
	}
	fm.entries = append(fm.entries, DirectForward{
		ListenPort: listenPort,
		Target:     target,
		CreatedAt:  time.Now().UTC(),
	})
	return fm.save()
}

// Remove deletes a rule by listen port.
func (fm *ForwardsManager) Remove(listenPort int) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()

	for i, e := range fm.entries {
		if e.ListenPort == listenPort {
			fm.entries = append(fm.entries[:i], fm.entries[i+1:]...)
			return fm.save()
		}
	}
	return fmt.Errorf("no forward rule for port %d", listenPort)
}

// List returns a copy of all rules.
func (fm *ForwardsManager) List() []DirectForward {
	fm.mu.RLock()
	defer fm.mu.RUnlock()
	out := make([]DirectForward, len(fm.entries))
	copy(out, fm.entries)
	return out
}

// startDirectListeners starts one TCP listener per forward rule.
// Called once at server startup.
func (s *Server) startDirectListeners() {
	for _, f := range s.forwards.List() {
		go s.startDirectListener(f)
	}
}

// startDirectListener binds a port and proxies whitelisted connections to the target.
func (s *Server) startDirectListener(f DirectForward) {
	addr := fmt.Sprintf(":%d", f.ListenPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("direct forward :%d → %s: listen error: %v", f.ListenPort, f.Target, err)
		s.beMu.Lock()
		s.bindErrors[f.ListenPort] = err.Error()
		s.beMu.Unlock()
		return
	}

	s.beMu.Lock()
	delete(s.bindErrors, f.ListenPort)
	s.beMu.Unlock()

	s.dlMu.Lock()
	s.directListeners[f.ListenPort] = ln
	s.dlMu.Unlock()

	log.Printf("direct forward :%d → %s (whitelist-only)", f.ListenPort, f.Target)

	go func() {
		<-s.done
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleDirectConn(conn, f.Target)
	}
}

// stopDirectListener closes the listener for a given port and removes it from the map.
func (s *Server) stopDirectListener(listenPort int) {
	s.dlMu.Lock()
	ln, ok := s.directListeners[listenPort]
	if ok {
		delete(s.directListeners, listenPort)
	}
	s.dlMu.Unlock()
	if ok {
		ln.Close()
	}

	s.beMu.Lock()
	delete(s.bindErrors, listenPort)
	s.beMu.Unlock()
}

// handleDirectConn checks the whitelist and proxies if allowed.
func (s *Server) handleDirectConn(conn net.Conn, target string) {
	defer conn.Close()

	remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())

	name, ok := s.whitelist.Contains(remoteIP)
	if !ok {
		log.Printf("direct: rejected non-whitelisted IP %s", remoteIP)
		return
	}

	// Track this connection so it can be killed if the IP is removed from the whitelist.
	s.dcMu.Lock()
	if s.directConns[remoteIP] == nil {
		s.directConns[remoteIP] = make(map[net.Conn]struct{})
	}
	s.directConns[remoteIP][conn] = struct{}{}
	s.dcMu.Unlock()
	defer func() {
		s.dcMu.Lock()
		delete(s.directConns[remoteIP], conn)
		s.dcMu.Unlock()
	}()

	backend, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		log.Printf("direct: %s → %s: dial error: %v", name, target, err)
		return
	}
	defer backend.Close()

	log.Printf("direct: %s (%s) → %s", name, remoteIP, target)

	done := make(chan struct{}, 2)
	go func() { io.Copy(backend, conn); done <- struct{}{} }()  //nolint:errcheck
	go func() { io.Copy(conn, backend); done <- struct{}{} }()  //nolint:errcheck
	<-done
}

// killDirectConns closes all active direct-forward connections from the given IP.
func (s *Server) killDirectConns(ip string) {
	s.dcMu.Lock()
	conns := s.directConns[ip]
	delete(s.directConns, ip)
	s.dcMu.Unlock()

	for conn := range conns {
		conn.Close()
		log.Printf("direct: killed connection from de-whitelisted IP %s", ip)
	}
}
