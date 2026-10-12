package server

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
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
	expose          *exposeSet // nil unless Expose == "labeled"
	sessions        *sessionSet
	tunPreAuth      chan struct{} // pre-auth slots on the tunnel port
	sshPreAuth      chan struct{} // pre-auth slots on the SSH port
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
	MetricsAddr     string // e.g. "0.0.0.0:9100"; empty = default, "off" = no HTTP API
	ACLPath         string // path to acl.yaml — empty to disable
	APIKey          string // bearer key for /api/*, /ui and /plugin/*
	ForwardBindAddr string // IP to bind direct-forward listeners to; empty = 0.0.0.0 (all interfaces)
	SSHAddr         string // embedded SSH server, e.g. ":2222"; empty = default, "off" = disabled

	// MaxSessionsPerClient caps TUN leases held by one name (token or
	// whitelist entry), so one credential can't take the whole address pool.
	// 0 = unlimited (the default, as before).
	MaxSessionsPerClient int

	// Store paths; empty = the /etc/auth-vpn defaults. ui-dev points these at
	// local files so it never touches a real server's state.
	WhitelistPath string
	ForwardsPath  string
	SSHKeysPath   string

	// Pushed to TUN clients in AUTH_OK. Auto-detected inside Kubernetes when empty.
	PushRoutes []string
	PushDNS    *protocol.DNSConfig
	NoPush     bool // disable pushing (and auto-detection) entirely

	// Expose is "all" (default: anything the server can route) or "labeled":
	// only Kubernetes Services labelled auth-vpn.io/expose=true are reachable,
	// enforced per packet (TUN) and per dial (proxy).
	Expose     string
	clusterDNS string // kube-dns IP detected from resolv.conf, if any

	// UIOnly runs the HTTP API/dashboard only — no TUN, no IP forwarding, no TLS
	// tunnel listener, no control socket, no SSH server. Lets the dashboard be
	// reviewed locally without root or a real deployment.
	UIOnly bool
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
	if cfg.WhitelistPath == "" {
		cfg.WhitelistPath = WhitelistFile
	}
	if cfg.ForwardsPath == "" {
		cfg.ForwardsPath = ForwardsFile
	}
	if cfg.SSHKeysPath == "" {
		cfg.SSHKeysPath = SSHKeysFile
	}
	if cfg.UIOnly {
		return // dev mode: never write the real server.yaml, no push detection
	}
	cfg.persistAutoDefaults()
	cfg.resolvePush()
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
	if sc.SSHAddr == "" && cfg.SSHAddr != "" && cfg.SSHAddr != "off" { // a one-off --ssh-addr off isn't persisted
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

	switch cfg.Expose {
	case "", ExposeAll, ExposeLabeled:
	default:
		return nil, fmt.Errorf("invalid expose %q (want %q or %q)", cfg.Expose, ExposeAll, ExposeLabeled)
	}

	baseIP, err := baseIPFromSubnet(cfg.Subnet)
	if err != nil {
		return nil, err
	}

	tm, err := auth.NewManager(cfg.TokensPath)
	if err != nil {
		return nil, fmt.Errorf("load tokens: %w", err)
	}

	wm, err := NewWhitelistManager(cfg.WhitelistPath)
	if err != nil {
		log.Printf("warning: load whitelist: %v (IP whitelist disabled)", err)
		wm, _ = NewWhitelistManager("")
	}

	fm, err := NewForwardsManager(cfg.ForwardsPath)
	if err != nil {
		log.Printf("warning: load forwards: %v (direct forwards disabled)", err)
		fm, _ = NewForwardsManager("")
	}

	km, err := NewSSHKeysManager(cfg.SSHKeysPath)
	if err != nil {
		log.Printf("warning: load ssh keys: %v", err)
		km, _ = NewSSHKeysManager("")
	}

	var aclEngine *acl.Engine
	if cfg.ACLPath != "" {
		// Fail closed, but stay up: a broken acl.yaml must not silently mean
		// "allow all", and refusing to start would take the dashboard down too.
		aclEngine, err = acl.Load(cfg.ACLPath)
		if err != nil {
			aclEngine.DenyAll()
			log.Printf("ERROR: ACL %s is invalid (%v): denying all tunnel traffic until it is fixed (then reload with SIGHUP or restart)", cfg.ACLPath, err)
		}
	}

	var exposed *exposeSet
	if cfg.Expose == ExposeLabeled {
		dnsIP := cfg.clusterDNS
		if cfg.PushDNS != nil {
			dnsIP = cfg.PushDNS.Server
		}
		exposed = newExposeSet(dnsIP)
		if _, p, err := net.SplitHostPort(cfg.MetricsAddr); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				exposed.allowSelf(cfg.ServerIP, uint16(n))
			}
		}
		log.Printf("expose=labeled: only Services labelled %s=true are reachable", exposeLabel)
	}

	return &Server{
		expose:          exposed,
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
		sessions:        newSessionSet(),
		tunPreAuth:      make(chan struct{}, maxPreAuth),
		sshPreAuth:      make(chan struct{}, maxPreAuth),
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
	if s.cfg.UIOnly {
		// ponytail: dev-only path, no TUN/TLS/control-socket/SSH — just the
		// dashboard, so `server ui-dev` needs no root and no real deployment.
		if s.cfg.MetricsAddr == "" || s.cfg.MetricsAddr == "off" {
			return fmt.Errorf("ui-only mode requires an HTTP API address")
		}
		log.Printf("ui-dev mode: dashboard only, no TUN/TLS tunnel")
		s.startHTTPAPI() // blocks until s.done closes it
		return nil
	}

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
	if s.expose != nil {
		go s.watchExposed()
	}

	if s.cfg.MetricsAddr != "off" {
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

	acceptLoop(ln, s.tunPreAuth, s.done, s.handleConn)
	s.wg.Wait()
	s.tun.Close()
	os.Remove(SocketFile) //nolint:errcheck
	return nil
}

// Tokens returns the token manager (for CLI token sub-commands).
func (s *Server) Tokens() *auth.Manager { return s.tokens }

// ConnectedClients returns a snapshot of connected clients.
func (s *Server) ConnectedClients() []clientInfo { return s.clients.Snapshot() }

// TokenManager exposes the auth.Manager for use by the CLI.
func (s *Server) TokenManager() *auth.Manager { return s.tokens }

// handleConn authenticates one client connection and starts forwarding.
// release frees the connection's pre-auth slot; it is called as soon as the
// client has authenticated (or on return).
func (s *Server) handleConn(conn net.Conn, release func()) {
	s.wg.Add(1)
	defer s.wg.Done()
	defer release()
	remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	defer conn.Close()

	// The TLS handshake and the AUTH frame must both arrive within the deadline;
	// a peer that connects and says nothing is dropped instead of held forever.
	_ = conn.SetDeadline(time.Now().Add(preAuthTimeout))

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
	var clientName, via string
	if wlName, ok := s.whitelist.Contains(remoteIP); ok {
		clientName, via = wlName, viaWhitelist
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
		clientName, via = tok.Name, viaToken
	}
	_ = conn.SetDeadline(time.Time{}) // authenticated: the reaper/pings take over
	release()

	// Proxy mode: no TUN, no IP assignment — just TCP port forwarding.
	if req.Mode == "proxy" {
		if err := tunnel.WriteFrame(conn, protocol.TypeAuthOK, protocol.Encode(protocol.AuthOKResponse{})); err != nil {
			log.Printf("send AUTH_OK (proxy) to %s: %v", clientName, err)
			return
		}
		defer s.sessions.add(&session{name: clientName, via: via, mode: "proxy", remote: remoteIP, conn: conn})()
		s.handleProxyConn(conn, clientName)
		return
	}

	client, err := s.clients.Assign(clientName, conn, s.cfg.MaxSessionsPerClient)
	if err != nil {
		log.Printf("assign IP for %s: %v", clientName, err)
		_ = tunnel.WriteFrame(conn, protocol.TypeAuthFail,
			protocol.Encode(protocol.AuthFailResponse{Reason: "no IP available"}))
		return
	}
	defer s.clients.Remove(client.ip)
	defer s.sessions.add(&session{name: clientName, via: via, mode: "tun", remote: remoteIP, conn: conn})()

	s.metrics.IncConnected()
	defer s.metrics.DecConnected()

	resp := protocol.AuthOKResponse{
		ClientIP: client.ip,
		ServerIP: s.cfg.ServerIP,
		Subnet:   s.cfg.Subnet,
		Routes:   s.pushRoutes(),
		DNS:      s.cfg.PushDNS,
	}
	if err := client.writeFrame(protocol.TypeAuthOK, protocol.Encode(resp)); err != nil {
		log.Printf("send AUTH_OK to %s: %v", clientName, err)
		return
	}
	log.Printf("client connected: %s → %s", clientName, client.ip)

	// Forward TCP → TUN.
	go s.forwardToTUN(client)

	// Forward TUN → TCP via sendCh (filled by routeFromTUN).
	for pkt := range client.sendCh {
		if err := client.writeFrame(protocol.TypeIPPacket, pkt); err != nil {
			break
		}
		client.bytesOut.Add(int64(len(pkt)))
		s.metrics.AddBytesOut(len(pkt))
	}
	log.Printf("client disconnected: %s", clientName)
}

// forwardToTUN reads IP packets from a client TCP conn and writes them to TUN.
func (s *Server) forwardToTUN(c *connectedClient) {
	defer func() {
		c.dead.Store(true) // must be set before closing sendCh
		close(c.sendCh)
	}()
	for {
		msgType, payload, err := tunnel.ReadFrame(c.conn)
		if err != nil {
			return
		}
		c.Touch()
		switch msgType {
		case protocol.TypeIPPacket:
			if s.expose != nil && !s.expose.allowPacket(payload) {
				s.metrics.IncDropped()
				continue
			}
			// ACL is checked on the way in, where the destination port is the
			// service the device is reaching (not the port it chose for itself).
			if s.acl != nil && !s.acl.Allow(c.name, payload) {
				s.metrics.IncDropped()
				continue
			}
			if _, err := s.tun.Write(payload); err != nil {
				log.Printf("write to TUN: %v", err)
				return
			}
			c.bytesIn.Add(int64(len(payload)))
			s.metrics.AddBytesIn(len(payload))
		case protocol.TypePing:
			_ = c.writeFrame(protocol.TypePong, nil)
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

		// dead is set before sendCh is closed; skip to avoid a panic on send-to-closed-chan.
		if client.dead.Load() {
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
			stale := s.clients.StaleConns(3 * time.Minute)
			for _, conn := range stale {
				log.Printf("reaper: closing idle connection from %s", conn.RemoteAddr())
				conn.Close()
			}
		case <-s.done:
			return
		}
	}
}

// pushRoutes is what TUN clients are told to route: configured routes plus,
// in labeled mode, one /32 per exposed Service. Computed per connection, so
// newly labelled Services reach clients on their next (re)connect.
func (s *Server) pushRoutes() []string {
	if s.cfg.NoPush {
		return nil
	}
	if s.expose == nil {
		return s.cfg.PushRoutes
	}
	return append(append([]string{}, s.cfg.PushRoutes...), s.expose.routes()...)
}
