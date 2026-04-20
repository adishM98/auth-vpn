package server

import (
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// connectedClient holds state for one authenticated tunnel client.
type connectedClient struct {
	name        string
	ip          string     // assigned tunnel IP, e.g. "10.0.0.2"
	conn        net.Conn
	sendCh      chan []byte // packets destined for this client
	connectedAt time.Time
	lastSeen    atomic.Int64 // Unix nanoseconds; updated on every received frame
	bytesIn     atomic.Int64 // bytes forwarded client → TUN
	bytesOut    atomic.Int64 // bytes forwarded TUN → client
}

// Touch records the current time as the last-seen timestamp.
func (c *connectedClient) Touch() {
	c.lastSeen.Store(time.Now().UnixNano())
}

// Stale returns true if no frame has been received within d.
func (c *connectedClient) Stale(d time.Duration) bool {
	return time.Now().UnixNano()-c.lastSeen.Load() > d.Nanoseconds()
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
		name:        name,
		ip:          ip,
		conn:        conn,
		sendCh:      make(chan []byte, 256),
		connectedAt: time.Now().UTC(),
	}
	c.lastSeen.Store(time.Now().UnixNano())
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
		out = append(out, clientInfo{
			Name:        c.name,
			IP:          c.ip,
			ConnectedAt: c.connectedAt,
			BytesIn:     c.bytesIn.Load(),
			BytesOut:    c.bytesOut.Load(),
		})
	}
	return out
}

// StaleConns returns connections that have not received any frame within d.
// The caller should close each returned connection.
func (r *clientRegistry) StaleConns(d time.Duration) []net.Conn {
	r.mu.RLock()
	defer r.mu.RUnlock()
	threshold := time.Now().Add(-d).UnixNano()
	var conns []net.Conn
	for _, c := range r.clients {
		if c.lastSeen.Load() < threshold {
			conns = append(conns, c.conn)
		}
	}
	return conns
}

type clientInfo struct {
	Name        string    `json:"name"`
	IP          string    `json:"ip"`
	ConnectedAt time.Time `json:"connected_at"`
	BytesIn     int64     `json:"bytes_in"`
	BytesOut    int64     `json:"bytes_out"`
}
