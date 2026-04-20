package server

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/adishM98/auth-vpn/internal/auth"
	"github.com/adishM98/auth-vpn/internal/tunnel"
	"github.com/adishM98/auth-vpn/pkg/protocol"
)

const (
	defaultServerIP = "10.0.0.1/24"
	defaultBaseIP   = "10.0.0"
	defaultSubnet   = "10.0.0.0/24"
	tunBufSize      = 65535
)

// Server is the auth-vpn tunnel server.
type Server struct {
	cfg     *Config
	tokens  *auth.Manager
	clients *clientRegistry
	limiter *auth.RateLimiter
	tun     *tunnel.Iface
}

// Config holds server runtime configuration.
type Config struct {
	Port     int
	TLSCert  string
	TLSKey   string
	TokensPath string
}

// New creates a Server using the given config.
func New(cfg *Config) (*Server, error) {
	tm, err := auth.NewManager(cfg.TokensPath)
	if err != nil {
		return nil, fmt.Errorf("load tokens: %w", err)
	}
	return &Server{
		cfg:     cfg,
		tokens:  tm,
		clients: newClientRegistry(defaultBaseIP),
		limiter: auth.NewRateLimiter(),
	}, nil
}

// Start creates the TUN interface, then listens for client connections.
func (s *Server) Start() error {
	if err := tunnel.EnableForwarding(); err != nil {
		log.Printf("warning: could not enable IP forwarding: %v", err)
	}

	var err error
	s.tun, err = tunnel.NewTUN(defaultServerIP)
	if err != nil {
		return fmt.Errorf("create server tun: %w", err)
	}
	log.Printf("TUN interface %s up at %s", s.tun.Name(), defaultServerIP)

	// Start reading from TUN and routing to clients.
	go s.routeFromTUN()

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

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept error: %v", err)
			continue
		}
		go s.handleConn(conn)
	}
}

// Tokens returns the token manager (for CLI token sub-commands).
func (s *Server) Tokens() *auth.Manager { return s.tokens }

// Clients returns a snapshot of connected clients.
func (s *Server) ConnectedClients() []clientInfo { return s.clients.Snapshot() }

// handleConn authenticates one client connection and starts forwarding.
func (s *Server) handleConn(conn net.Conn) {
	remoteIP := strings.Split(conn.RemoteAddr().String(), ":")[0]
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

	tok, err := s.tokens.Validate(req.Token)
	if err != nil {
		s.limiter.RecordFailure(remoteIP)
		log.Printf("auth failed from %s: %v", remoteIP, err)
		_ = tunnel.WriteFrame(conn, protocol.TypeAuthFail,
			protocol.Encode(protocol.AuthFailResponse{Reason: err.Error()}))
		return
	}
	s.limiter.Reset(remoteIP)

	client, err := s.clients.Assign(tok.Name, conn)
	if err != nil {
		log.Printf("assign IP for %s: %v", tok.Name, err)
		_ = tunnel.WriteFrame(conn, protocol.TypeAuthFail,
			protocol.Encode(protocol.AuthFailResponse{Reason: "no IP available"}))
		return
	}
	defer s.clients.Remove(client.ip)

	resp := protocol.AuthOKResponse{
		ClientIP: client.ip,
		ServerIP: "10.0.0.1",
		Subnet:   defaultSubnet,
	}
	if err := tunnel.WriteFrame(conn, protocol.TypeAuthOK, protocol.Encode(resp)); err != nil {
		log.Printf("send AUTH_OK to %s: %v", tok.Name, err)
		return
	}
	log.Printf("client connected: %s → %s", tok.Name, client.ip)

	// Forward TCP → TUN.
	go s.forwardToTUN(client)

	// Forward TUN → TCP via sendCh (filled by routeFromTUN).
	for pkt := range client.sendCh {
		if err := tunnel.WriteFrame(conn, protocol.TypeIPPacket, pkt); err != nil {
			break
		}
	}
	log.Printf("client disconnected: %s", tok.Name)
}

// forwardToTUN reads IP packets from a client TCP conn and writes them to TUN.
func (s *Server) forwardToTUN(c *connectedClient) {
	defer close(c.sendCh)
	for {
		msgType, payload, err := tunnel.ReadFrame(c.conn)
		if err != nil {
			return
		}
		switch msgType {
		case protocol.TypeIPPacket:
			if _, err := s.tun.Write(payload); err != nil {
				log.Printf("write to TUN: %v", err)
				return
			}
		case protocol.TypePing:
			_ = tunnel.WriteFrame(c.conn, protocol.TypePong, nil)
		case protocol.TypeDisconnect:
			return
		}
	}
}

// routeFromTUN reads packets from the TUN interface and fans them to the
// appropriate client based on destination IP in the IPv4 header.
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
			continue // no client for this IP
		}

		select {
		case client.sendCh <- pkt:
		default:
			// client send buffer full — drop packet
		}
	}
}

// TokenManager exposes the auth.Manager for use by the CLI.
func (s *Server) TokenManager() *auth.Manager { return s.tokens }
