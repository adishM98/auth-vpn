package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songgao/water"
	"golang.org/x/crypto/ssh"

	"github.com/adishM98/auth-vpn/internal/auth"
	"github.com/adishM98/auth-vpn/internal/server/acl"
	"github.com/adishM98/auth-vpn/internal/tunnel"
	"github.com/adishM98/auth-vpn/pkg/protocol"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	tm, err := auth.NewManager(filepath.Join(t.TempDir(), "tokens.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	wl, _ := NewWhitelistManager("")
	return &Server{
		cfg:         &Config{Subnet: "10.8.0.0/24", ServerIP: "10.8.0.1", MaxSessionsPerClient: defaultMaxSessionsPerClient, APIKey: "k"},
		tokens:      tm,
		whitelist:   wl,
		clients:     newClientRegistry("10.8.0"),
		limiter:     auth.NewRateLimiter(),
		metrics:     newMetrics(),
		sessions:    newSessionSet(),
		directConns: make(map[string]map[net.Conn]struct{}),
		done:        make(chan struct{}),
	}
}

func withACL(t *testing.T, s *Server, policy string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "acl.yaml")
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := acl.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.acl = e
}

const onlyPostgres = "default_policy: deny\nrules:\n  - device: dev-alice\n    allow:\n      - proto: tcp\n        port: 5432\n"

// connectProxy authenticates a proxy-mode session over a pipe and returns
// the client end.
func connectProxy(t *testing.T, s *Server, token string) net.Conn {
	t.Helper()
	srv, cli := net.Pipe()
	go s.handleConn(srv, func() {})
	if err := tunnel.WriteFrame(cli, protocol.TypeAuth, protocol.Encode(protocol.AuthRequest{Token: token, Mode: "proxy"})); err != nil {
		t.Fatal(err)
	}
	if typ, _, err := tunnel.ReadFrame(cli); err != nil || typ != protocol.TypeAuthOK {
		t.Fatalf("auth: type %x err %v", typ, err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli
}

// dial asks the proxy session for host:port and returns the reply frame type.
func dial(t *testing.T, cli net.Conn, id uint32, addr string) byte {
	t.Helper()
	host, port, _ := net.SplitHostPort(addr)
	var p uint16
	for _, c := range port {
		p = p*10 + uint16(c-'0')
	}
	if err := tunnel.WriteFrame(cli, protocol.TypeProxyDial, protocol.Encode(protocol.ProxyDialRequest{StreamID: id, Host: host, Port: p})); err != nil {
		t.Fatal(err)
	}
	for {
		_ = cli.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, _, err := tunnel.ReadFrame(cli)
		if err != nil {
			t.Fatal(err)
		}
		if typ == protocol.TypeProxyOK || typ == protocol.TypeProxyFail {
			return typ
		}
	}
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, c) //nolint:errcheck
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

// recordingTUN stands in for the server TUN device and records writes.
type recordingTUN struct {
	mu  sync.Mutex
	pkt [][]byte
}

func (r *recordingTUN) Read([]byte) (int, error) { select {} }
func (r *recordingTUN) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pkt = append(r.pkt, append([]byte(nil), p...))
	return len(p), nil
}
func (r *recordingTUN) Close() error { return nil }

func tcpSYN(srcPort, dstPort uint16) []byte {
	p := make([]byte, 40)
	p[0], p[9] = 0x45, 6
	copy(p[12:16], net.IPv4(10, 8, 0, 2).To4())
	copy(p[16:20], net.IPv4(10, 0, 0, 5).To4())
	binary.BigEndian.PutUint16(p[20:], srcPort)
	binary.BigEndian.PutUint16(p[22:], dstPort)
	return p
}

func TestACLEnforcement(t *testing.T) {
	t.Run("TUN: a restricted device's packets to other ports never reach the network", func(t *testing.T) {
		s := newTestServer(t)
		withACL(t, s, onlyPostgres)
		rec := &recordingTUN{}
		s.tun = &tunnel.Iface{Interface: &water.Interface{ReadWriteCloser: rec}}

		srv, cli := net.Pipe()
		c, _ := s.clients.Assign("dev-alice", srv, 0)
		done := make(chan struct{})
		go func() { s.forwardToTUN(c); close(done) }()
		for _, p := range [][]byte{tcpSYN(5432, 22), tcpSYN(51000, 5432)} {
			if err := tunnel.WriteFrame(cli, protocol.TypeIPPacket, p); err != nil {
				t.Fatal(err)
			}
		}
		_ = tunnel.WriteFrame(cli, protocol.TypeDisconnect, nil)
		<-done

		if len(rec.pkt) != 1 || binary.BigEndian.Uint16(rec.pkt[0][22:]) != 5432 {
			t.Fatalf("TUN got %d packets, want only the one to :5432", len(rec.pkt))
		}
	})

	t.Run("proxy mode applies the same ACL", func(t *testing.T) {
		s := newTestServer(t)
		withACL(t, s, onlyPostgres)
		raw, _ := s.tokens.Add("dev-alice", nil, false)
		cli := connectProxy(t, s, raw)
		if typ := dial(t, cli, 1, listenLoopback(t).Addr().String()); typ != protocol.TypeProxyFail {
			t.Fatal("proxy dial to a port outside the ACL succeeded")
		}
	})
}

func TestProxyStreams(t *testing.T) {
	t.Run("a stream id can't be reused while open", func(t *testing.T) {
		s := newTestServer(t)
		raw, _ := s.tokens.Add("alice", nil, false)
		cli := connectProxy(t, s, raw)
		addr := listenLoopback(t).Addr().String()
		if dial(t, cli, 7, addr) != protocol.TypeProxyOK {
			t.Fatal("first dial failed")
		}
		if dial(t, cli, 7, addr) != protocol.TypeProxyFail {
			t.Fatal("reused stream id accepted")
		}
	})
}

func TestRevocationEndsSessions(t *testing.T) {
	waitClosed := func(t *testing.T, c net.Conn) {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			if _, _, err := tunnel.ReadFrame(c); err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					t.Fatal("session still open")
				}
				return
			}
		}
	}

	t.Run("revoking a token closes its proxy session", func(t *testing.T) {
		s := newTestServer(t)
		raw, _ := s.tokens.Add("alice", nil, false)
		cli := connectProxy(t, s, raw)

		w := httptest.NewRecorder()
		s.handleAPITokenDelete(w, httptest.NewRequest(http.MethodDelete, "/api/tokens/alice", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("revoke: %d", w.Code)
		}
		waitClosed(t, cli)
	})

	t.Run("kick finds proxy sessions", func(t *testing.T) {
		s := newTestServer(t)
		raw, _ := s.tokens.Add("alice", nil, false)
		cli := connectProxy(t, s, raw)

		w := httptest.NewRecorder()
		s.handleAPIClientDisconnect(w, httptest.NewRequest(http.MethodDelete, "/api/clients/alice", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("kick: %d", w.Code)
		}
		waitClosed(t, cli)
	})

	t.Run("removing a CIDR whitelist entry closes direct forwards from that range", func(t *testing.T) {
		s := newTestServer(t)
		a, b := net.Pipe()
		defer b.Close()
		s.directConns["10.0.0.7"] = map[net.Conn]struct{}{a: {}}
		s.killDirectConns("10.0.0.0/24")
		_ = a.SetWriteDeadline(time.Now().Add(time.Second)) // an open pipe would block forever
		if _, err := a.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal("connection from 10.0.0.7 survived removal of 10.0.0.0/24")
		}
	})
}

func TestPreAuth(t *testing.T) {
	t.Run("a peer that never sends AUTH is dropped", func(t *testing.T) {
		old := preAuthTimeout
		preAuthTimeout = 100 * time.Millisecond
		defer func() { preAuthTimeout = old }()

		s := newTestServer(t)
		srv, cli := net.Pipe()
		defer cli.Close()
		done := make(chan struct{})
		go func() { s.handleConn(srv, func() {}); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("silent connection held open")
		}
	})

	t.Run("one name can't take the whole address pool", func(t *testing.T) {
		r := newClientRegistry("10.8.0")
		for i := 0; i < 2; i++ {
			if _, err := r.Assign("ci", nil, 2); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.Assign("ci", nil, 2); err == nil {
			t.Fatal("third lease for the same name accepted")
		}
		if _, err := r.Assign("other", nil, 2); err != nil {
			t.Fatal("another name was locked out")
		}
	})
}

func TestOpenModeAPI(t *testing.T) {
	s := newTestServer(t)
	s.cfg.APIKey = ""
	h := s.withAuth(s.handleAPITokens)

	cases := []struct {
		name, host, origin, ctype string
		want                      int
	}{
		{"same-origin JSON from the dashboard", "localhost:9100", "http://localhost:9100", "application/json", http.StatusCreated},
		{"cross-site text/plain POST", "127.0.0.1:9100", "https://evil.example", "text/plain", http.StatusForbidden},
		{"DNS-rebound host name", "rebind.evil.example:9100", "", "application/json", http.StatusForbidden},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/tokens", strings.NewReader(`{"name":"t`+string(rune('a'+i))+`"}`))
			r.Host = c.host
			r.Header.Set("Content-Type", c.ctype)
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			w := httptest.NewRecorder()
			h(w, r)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d (%s)", w.Code, c.want, w.Body)
			}
		})
	}
}

func TestSystemAuthorizedKeys(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))

	for _, tc := range []struct {
		name, entry string
		ok          bool
	}{
		{"plain key is accepted", line, true},
		{"harmless option is accepted", "no-agent-forwarding " + line, true},
		{"restrict is refused", "restrict " + line, false},
		{"forced command is refused", `command="/usr/bin/backup" ` + line, false},
		{"source-limited key is refused", `from="192.0.2.1" ` + line, false},
		{"no-port-forwarding is refused", "no-port-forwarding,no-pty " + line, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "authorized_keys")
			_ = os.WriteFile(path, []byte(tc.entry+"\n"), 0o600)
			if got := authorizedKeysMatch(key, path); got != tc.ok {
				t.Fatalf("authorizedKeysMatch = %v, want %v", got, tc.ok)
			}
		})
	}
}

func TestDisableSwitches(t *testing.T) {
	cfg := &Config{SSHAddr: "off", MetricsAddr: "off", ForwardBindAddr: "127.0.0.1", UIOnly: true}
	cfg.applyDefaults()
	if cfg.SSHAddr != "off" || cfg.MetricsAddr != "off" {
		t.Fatalf("off switches overridden: ssh=%q api=%q", cfg.SSHAddr, cfg.MetricsAddr)
	}
	if cfg.WhitelistPath != WhitelistFile {
		t.Fatalf("default whitelist path = %q", cfg.WhitelistPath)
	}
}

func TestUpgradeCompatibility(t *testing.T) {
	create := func(s *Server, name string) (int, string) {
		r := httptest.NewRequest(http.MethodPost, "/api/tokens", strings.NewReader(`{"name":"`+name+`"}`))
		r.Header.Set("Authorization", "Bearer k")
		w := httptest.NewRecorder()
		s.withAuth(s.handleAPITokens)(w, r)
		var body struct{ Name string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Name
	}

	t.Run("colliding CI token names from older CLIs get a suffix, not an error", func(t *testing.T) {
		s := newTestServer(t)
		if code, _ := create(s, "gh-1-build-1"); code != http.StatusCreated {
			t.Fatal(code)
		}
		code, name := create(s, "gh-1-build-1")
		if code != http.StatusCreated || name == "gh-1-build-1" || !strings.HasPrefix(name, "gh-1-build-1-") {
			t.Fatalf("second matrix leg: %d %q", code, name)
		}
	})

	t.Run("a duplicate human-chosen name is still a conflict", func(t *testing.T) {
		s := newTestServer(t)
		create(s, "alice")
		if code, _ := create(s, "alice"); code != http.StatusConflict {
			t.Fatalf("status %d, want 409", code)
		}
	})

	t.Run("the bare key in Authorization still works", func(t *testing.T) {
		s := newTestServer(t)
		r := httptest.NewRequest(http.MethodGet, "/api/tokens", nil)
		r.Header.Set("Authorization", "k")
		w := httptest.NewRecorder()
		s.withAuth(s.handleAPITokens)(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
	})
}
