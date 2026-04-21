package server

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/adishM98/auth-vpn/internal/auth"
	"github.com/adishM98/auth-vpn/internal/server/acl"
	"github.com/adishM98/auth-vpn/internal/tunnel"
	"github.com/adishM98/auth-vpn/pkg/protocol"
)

const tunBufSize = 65535

// Server is the auth-vpn tunnel server.
type Server struct {
	cfg             *Config
	tokens          *auth.Manager
	whitelist       *WhitelistManager
	forwards        *ForwardsManager
	sshKeys         *SSHKeysManager
	directListeners map[int]net.Listener
	dlMu            sync.Mutex
	directConns     map[string]map[net.Conn]struct{} // remoteIP → active direct-forward conns
	dcMu            sync.Mutex
	bindErrors      map[int]string // port → bind error message for forwards that failed to start
	beMu            sync.RWMutex
	clients         *clientRegistry
	limiter         *auth.RateLimiter
	tun             *tunnel.Iface
	acl             *acl.Engine
	metrics         *Metrics
	done            chan struct{}
	wg              sync.WaitGroup
}

// Config holds server runtime configuration.
type Config struct {
	Port        int
	TLSCert     string
	TLSKey      string
	TokensPath  string
	Subnet      string // e.g. "10.0.0.0/24"
	ServerIP    string // e.g. "10.0.0.1"
	MetricsAddr     string // e.g. "localhost:9100" — empty to disable
	ACLPath         string // path to acl.yaml — empty to disable
	APIKey          string // bearer key for /tooljet/* — empty to disable
	ForwardBindAddr string // IP to bind direct-forward listeners to; empty = 0.0.0.0 (all interfaces)
	SSHAddr         string // address for embedded SSH server, e.g. ":2222"; empty = disabled
}

func (cfg *Config) applyDefaults() {
	if cfg.Subnet == "" {
		cfg.Subnet = "10.8.0.0/24"
	}
	if cfg.ServerIP == "" {
		cfg.ServerIP = "10.8.0.1"
	}
	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = DefaultMetricsAddr
	}
	if cfg.ForwardBindAddr == "" {
		cfg.ForwardBindAddr = outboundIP()
	}
	if cfg.SSHAddr == "" {
		cfg.SSHAddr = ":2222"
	}
	cfg.persistAutoDefaults()
}

// outboundIP returns the local IP the OS would use for outbound traffic.
// No data is actually sent — the UDP dial just triggers a routing lookup.
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// persistAutoDefaults writes auto-detected values back into server.yaml so they
// survive restarts and are visible to operators.
func (cfg *Config) persistAutoDefaults() {
	sc, err := LoadServerConfig(ServerConfigFile)
	if err != nil {
		return // not a server install or file unreadable
	}
	changed := false
	if sc.ForwardBindAddr == "" && cfg.ForwardBindAddr != "" {
		sc.ForwardBindAddr = cfg.ForwardBindAddr
		log.Printf("auto-detected forward_bind_addr: %s (saved to server.yaml)", cfg.ForwardBindAddr)
		changed = true
	}
	if sc.SSHAddr == "" && cfg.SSHAddr != "" {
		sc.SSHAddr = cfg.SSHAddr
		log.Printf("auto-set ssh_addr: %s (saved to server.yaml)", cfg.SSHAddr)
		changed = true
	}
	if !changed {
		return
	}
	if err := SaveServerConfig(ServerConfigFile, sc); err != nil {
		log.Printf("warning: persist server.yaml: %v", err)
	}
}

// baseIPFromSubnet derives "10.0.0" from "10.0.0.0/24".
func baseIPFromSubnet(subnet string) (string, error) {
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet %q: %w", subnet, err)
	}
	ip := ipNet.IP.String()
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return "", fmt.Errorf("unexpected IP format: %s", ip)
	}
	return strings.Join(parts[:3], "."), nil
}

// New creates a Server using the given config.
func New(cfg *Config) (*Server, error) {
	cfg.applyDefaults()

	baseIP, err := baseIPFromSubnet(cfg.Subnet)
	if err != nil {
		return nil, err
	}

	tm, err := auth.NewManager(cfg.TokensPath)
	if err != nil {
		return nil, fmt.Errorf("load tokens: %w", err)
	}

	wm, err := NewWhitelistManager(WhitelistFile)
	if err != nil {
		log.Printf("warning: load whitelist: %v (IP whitelist disabled)", err)
		wm, _ = NewWhitelistManager("")
	}

	fm, err := NewForwardsManager(ForwardsFile)
	if err != nil {
		log.Printf("warning: load forwards: %v (direct forwards disabled)", err)
		fm, _ = NewForwardsManager("")
	}

	km, err := NewSSHKeysManager(SSHKeysFile)
	if err != nil {
		log.Printf("warning: load ssh keys: %v", err)
		km, _ = NewSSHKeysManager("")
	}

	var aclEngine *acl.Engine
	if cfg.ACLPath != "" {
		aclEngine, err = acl.Load(cfg.ACLPath)
		if err != nil {
			log.Printf("warning: load ACL %s: %v (allowing all traffic)", cfg.ACLPath, err)
		}
	}

	return &Server{
		cfg:             cfg,
		tokens:          tm,
		whitelist:       wm,
		forwards:        fm,
		sshKeys:         km,
		directListeners: make(map[int]net.Listener),
		directConns:     make(map[string]map[net.Conn]struct{}),
		bindErrors:      make(map[int]string),
		clients:         newClientRegistry(baseIP),
		limiter:         auth.NewRateLimiter(),
		acl:             aclEngine,
		metrics:         newMetrics(),
		done:            make(chan struct{}),
	}, nil
}

// Shutdown signals the server to stop accepting connections and exit cleanly.
func (s *Server) Shutdown() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// Start creates the TUN interface, then listens for client connections.
func (s *Server) Start() error {
	if err := tunnel.EnableForwarding(); err != nil {
		log.Printf("warning: could not enable IP forwarding: %v", err)
	}

	serverTUNAddr := s.cfg.ServerIP + "/24"
	var err error
	s.tun, err = tunnel.NewTUN(serverTUNAddr)
	if err != nil {
		return fmt.Errorf("create server tun: %w", err)
	}
	log.Printf("TUN interface %s up at %s", s.tun.Name(), serverTUNAddr)

	go s.routeFromTUN()
	go s.startReaper()
	go s.startControlSocket()
	go s.startDirectListeners()
	go s.startSSHServer()

	if s.cfg.MetricsAddr != "" {
		go s.startHTTPAPI()
	}

	cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
	if err != nil {
		return fmt.Errorf("load TLS keypair: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	ln, err := tls.Listen("tcp", fmt.Sprintf(":%d", s.cfg.Port), tlsCfg)
	if err != nil {
		return fmt.Errorf("listen :%d: %w", s.cfg.Port, err)
	}
	log.Printf("auth-vpn server listening on :%d (TLS)", s.cfg.Port)

	// Signal handler — shutdown on SIGINT/SIGTERM, reload ACL on SIGHUP.
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		for sig := range sigs {
			switch sig {
			case syscall.SIGHUP:
				if s.acl != nil {
					if err := s.acl.Reload(); err != nil {
						log.Printf("ACL reload: %v", err)
					} else {
						log.Println("ACL reloaded")
					}
				}
			default:
				log.Println("shutting down...")
				s.Shutdown()
				ln.Close()
				return
			}
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				s.wg.Wait()
				s.tun.Close()
				os.Remove(SocketFile) //nolint:errcheck
				return nil
			default:
				log.Printf("accept error: %v", err)
				continue
			}
		}
		go s.handleConn(conn)
	}
}

// Tokens returns the token manager (for CLI token sub-commands).
func (s *Server) Tokens() *auth.Manager { return s.tokens }

// ConnectedClients returns a snapshot of connected clients.
func (s *Server) ConnectedClients() []clientInfo { return s.clients.Snapshot() }

// TokenManager exposes the auth.Manager for use by the CLI.
func (s *Server) TokenManager() *auth.Manager { return s.tokens }

// handleConn authenticates one client connection and starts forwarding.
func (s *Server) handleConn(conn net.Conn) {
	s.wg.Add(1)
	defer s.wg.Done()
	remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	defer conn.Close()

	if s.limiter.IsBanned(remoteIP) {
		log.Printf("blocked banned IP %s", remoteIP)
		return
	}

	// Expect AUTH frame first.
	msgType, payload, err := tunnel.ReadFrame(conn)
	if err != nil {
		log.Printf("read auth frame from %s: %v", remoteIP, err)
		return
	}
	if msgType != protocol.TypeAuth {
		log.Printf("unexpected frame type %x from %s", msgType, remoteIP)
		return
	}

	var req protocol.AuthRequest
	if err := protocol.Decode(payload, &req); err != nil {
		log.Printf("decode auth from %s: %v", remoteIP, err)
		return
	}

	// Check IP whitelist first — whitelisted IPs connect without a token.
	var clientName string
	if wlName, ok := s.whitelist.Contains(remoteIP); ok {
		clientName = wlName
		log.Printf("whitelisted IP %s connected as %q", remoteIP, clientName)
	} else {
		tok, err := s.tokens.Validate(req.Token)
		if err != nil {
			s.limiter.RecordFailure(remoteIP)
			s.metrics.IncAuthFailure()
			log.Printf("auth failed from %s: %v", remoteIP, err)
			_ = tunnel.WriteFrame(conn, protocol.TypeAuthFail,
				protocol.Encode(protocol.AuthFailResponse{Reason: err.Error()}))
			return
		}
		s.limiter.Reset(remoteIP)

		// Prevent concurrent use of the same token.
		if !s.tokens.TryClaim(req.Token) {
			log.Printf("token %q already in use, rejecting %s", tok.Name, remoteIP)
			_ = tunnel.WriteFrame(conn, protocol.TypeAuthFail,
				protocol.Encode(protocol.AuthFailResponse{Reason: "token already in use"}))
			return
		}
		defer s.tokens.Unclaim(req.Token)
		clientName = tok.Name
	}

	// Proxy mode: no TUN, no IP assignment — just TCP port forwarding.
	if req.Mode == "proxy" {
		if err := tunnel.WriteFrame(conn, protocol.TypeAuthOK, protocol.Encode(protocol.AuthOKResponse{})); err != nil {
			log.Printf("send AUTH_OK (proxy) to %s: %v", clientName, err)
			return
		}
		s.handleProxyConn(conn, clientName)
		return
	}

	client, err := s.clients.Assign(clientName, conn)
	if err != nil {
		log.Printf("assign IP for %s: %v", clientName, err)
		_ = tunnel.WriteFrame(conn, protocol.TypeAuthFail,
			protocol.Encode(protocol.AuthFailResponse{Reason: "no IP available"}))
		return
	}
	defer s.clients.Remove(client.ip)

	s.metrics.IncConnected()
	defer s.metrics.DecConnected()

	resp := protocol.AuthOKResponse{
		ClientIP: client.ip,
		ServerIP: s.cfg.ServerIP,
		Subnet:   s.cfg.Subnet,
	}
	if err := tunnel.WriteFrame(conn, protocol.TypeAuthOK, protocol.Encode(resp)); err != nil {
		log.Printf("send AUTH_OK to %s: %v", clientName, err)
		return
	}
	log.Printf("client connected: %s → %s", clientName, client.ip)

	// Forward TCP → TUN.
	go s.forwardToTUN(client)

	// Forward TUN → TCP via sendCh (filled by routeFromTUN).
	for pkt := range client.sendCh {
		if err := tunnel.WriteFrame(conn, protocol.TypeIPPacket, pkt); err != nil {
			break
		}
		client.bytesOut.Add(int64(len(pkt)))
		s.metrics.AddBytesOut(len(pkt))
	}
	log.Printf("client disconnected: %s", clientName)
}

// forwardToTUN reads IP packets from a client TCP conn and writes them to TUN.
func (s *Server) forwardToTUN(c *connectedClient) {
	defer close(c.sendCh)
	for {
		msgType, payload, err := tunnel.ReadFrame(c.conn)
		if err != nil {
			return
		}
		c.Touch() // P0-B: update last-seen timestamp
		switch msgType {
		case protocol.TypeIPPacket:
			if _, err := s.tun.Write(payload); err != nil {
				log.Printf("write to TUN: %v", err)
				return
			}
			c.bytesIn.Add(int64(len(payload)))
			s.metrics.AddBytesIn(len(payload))
		case protocol.TypePing:
			_ = tunnel.WriteFrame(c.conn, protocol.TypePong, nil)
		case protocol.TypeDisconnect:
			return
		}
	}
}

// routeFromTUN reads packets from the TUN interface and routes them to the
// appropriate client based on the destination IP in the IPv4 header.
func (s *Server) routeFromTUN() {
	buf := make([]byte, tunBufSize)
	for {
		n, err := s.tun.Read(buf)
		if err != nil {
			log.Printf("TUN read error: %v", err)
			return
		}
		if n < 20 {
			continue // too short to be a valid IPv4 packet
		}

		pkt := make([]byte, n)
		copy(pkt, buf[:n])

		destIP := fmt.Sprintf("%d.%d.%d.%d", pkt[16], pkt[17], pkt[18], pkt[19])
		client := s.clients.GetByIP(destIP)
		if client == nil {
			continue
		}

		// Step 3: ACL enforcement.
		if s.acl != nil && !s.acl.Allow(client.name, pkt) {
			s.metrics.IncDropped()
			continue
		}

		select {
		case client.sendCh <- pkt:
		default:
			s.metrics.IncDropped()
		}
	}
}

// startReaper periodically closes connections that have gone idle.
// P0-B: zombie connection cleanup.
func (s *Server) startReaper() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			stale := s.clients.StaleConns(90 * time.Second)
			for _, conn := range stale {
				log.Printf("reaper: closing idle connection from %s", conn.RemoteAddr())
				conn.Close()
			}
		case <-s.done:
			return
		}
	}
}
