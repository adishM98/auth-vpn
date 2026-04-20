package server

import (
	"fmt"
	"net"
	"sync"
)

// connectedClient holds state for one authenticated tunnel client.
type connectedClient struct {
	name   string
	ip     string       // assigned tunnel IP, e.g. "10.0.0.2"
	conn   net.Conn
	sendCh chan []byte   // packets destined for this client
}

// clientRegistry manages IP assignment and per-client lookup.
type clientRegistry struct {
	mu      sync.RWMutex
	clients map[string]*connectedClient // keyed by tunnel IP
	nextIP  uint8                        // next host octet to assign (starts at 2)
	baseIP  string                       // e.g. "10.0.0"
}

func newClientRegistry(baseIP string) *clientRegistry {
	return &clientRegistry{
		clients: make(map[string]*connectedClient),
		nextIP:  2,
		baseIP:  baseIP,
	}
}

// Assign allocates a tunnel IP and registers the client. Returns the client.
func (r *clientRegistry) Assign(name string, conn net.Conn) (*connectedClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.nextIP > 254 {
		return nil, fmt.Errorf("tunnel IP pool exhausted")
	}
	ip := fmt.Sprintf("%s.%d", r.baseIP, r.nextIP)
	r.nextIP++

	c := &connectedClient{
		name:   name,
		ip:     ip,
		conn:   conn,
		sendCh: make(chan []byte, 256),
	}
	r.clients[ip] = c
	return c, nil
}

// GetByIP returns the client assigned to the given tunnel IP, or nil.
func (r *clientRegistry) GetByIP(ip string) *connectedClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.clients[ip]
}

// Remove deregisters a client by tunnel IP.
func (r *clientRegistry) Remove(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, ip)
}

// Snapshot returns a copy of all connected clients for display.
func (r *clientRegistry) Snapshot() []clientInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []clientInfo
	for _, c := range r.clients {
		out = append(out, clientInfo{Name: c.name, IP: c.ip})
	}
	return out
}

type clientInfo struct {
	Name string
	IP   string
}
