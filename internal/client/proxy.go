package client

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/adishM98/auth-vpn/internal/tunnel"
	"github.com/adishM98/auth-vpn/pkg/protocol"
)

// ForwardRule maps a local port to a remote host:port through the tunnel.
// Usage: --forward 5432:10.8.0.1:5432
type ForwardRule struct {
	LocalPort  int
	RemoteHost string
	RemotePort int
}

// proxyMux multiplexes multiple TCP streams over a single TLS connection.
type proxyMux struct {
	conn    net.Conn
	writeMu sync.Mutex

	// pendingDials waiting for ProxyOK/ProxyFail from server
	pendingMu sync.Mutex
	pending   map[uint32]chan error

	// active forwarded TCP connections keyed by stream ID
	streamsMu sync.Mutex
	streams   map[uint32]net.Conn

	nextID uint32 // accessed atomically

	ctx    context.Context
	cancel context.CancelFunc
}

func newProxyMux(conn net.Conn, ctx context.Context, cancel context.CancelFunc) *proxyMux {
	return &proxyMux{
		conn:    conn,
		pending: make(map[uint32]chan error),
		streams: make(map[uint32]net.Conn),
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (m *proxyMux) writeFrame(msgType byte, payload []byte) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	return tunnel.WriteFrame(m.conn, msgType, payload)
}

// dialRemote requests the server to open a TCP connection to host:port.
func (m *proxyMux) dialRemote(host string, port int) (uint32, error) {
	id := atomic.AddUint32(&m.nextID, 1)

	ch := make(chan error, 1)
	m.pendingMu.Lock()
	m.pending[id] = ch
	m.pendingMu.Unlock()

	req := protocol.ProxyDialRequest{StreamID: id, Host: host, Port: uint16(port)}
	if err := m.writeFrame(protocol.TypeProxyDial, protocol.Encode(req)); err != nil {
		m.pendingMu.Lock()
		delete(m.pending, id)
		m.pendingMu.Unlock()
		return 0, err
	}

	select {
	case err := <-ch:
		return id, err
	case <-time.After(10 * time.Second):
		m.pendingMu.Lock()
		delete(m.pending, id)
		m.pendingMu.Unlock()
		return 0, fmt.Errorf("dial timeout")
	case <-m.ctx.Done():
		return 0, fmt.Errorf("connection closed")
	}
}

// readLoop reads frames from the TLS connection and dispatches them.
// Runs in its own goroutine; calls cancel() on exit.
func (m *proxyMux) readLoop() {
	defer m.cancel()
	for {
		msgType, payload, err := tunnel.ReadFrame(m.conn)
		if err != nil {
			return
		}

		switch msgType {
		case protocol.TypeProxyOK:
			var ok protocol.ProxyDialOK
			if err := protocol.Decode(payload, &ok); err != nil {
				continue
			}
			m.pendingMu.Lock()
			ch := m.pending[ok.StreamID]
			delete(m.pending, ok.StreamID)
			m.pendingMu.Unlock()
			if ch != nil {
				ch <- nil
			}

		case protocol.TypeProxyFail:
			var fail protocol.ProxyDialFail
			if err := protocol.Decode(payload, &fail); err != nil {
				continue
			}
			m.pendingMu.Lock()
			ch := m.pending[fail.StreamID]
			delete(m.pending, fail.StreamID)
			m.pendingMu.Unlock()
			if ch != nil {
				ch <- fmt.Errorf("%s", fail.Reason)
			}

		case protocol.TypeProxyData:
			if len(payload) < 4 {
				continue
			}
			id := binary.BigEndian.Uint32(payload[:4])
			data := payload[4:]
			m.streamsMu.Lock()
			c := m.streams[id]
			m.streamsMu.Unlock()
			if c != nil && len(data) > 0 {
				c.Write(data) //nolint:errcheck
			}

		case protocol.TypeProxyClose:
			if len(payload) < 4 {
				continue
			}
			id := binary.BigEndian.Uint32(payload[:4])
			m.streamsMu.Lock()
			c := m.streams[id]
			delete(m.streams, id)
			m.streamsMu.Unlock()
			if c != nil {
				c.Close()
			}

		case protocol.TypePing:
			m.writeFrame(protocol.TypePong, nil) //nolint:errcheck

		case protocol.TypeDisconnect:
			return
		}
	}
}

// handleLocalConn services one accepted local TCP connection for a forward rule.
func (m *proxyMux) handleLocalConn(localConn net.Conn, rule ForwardRule) {
	defer localConn.Close()

	id, err := m.dialRemote(rule.RemoteHost, rule.RemotePort)
	if err != nil {
		log.Printf("proxy: dial %s:%d: %v", rule.RemoteHost, rule.RemotePort, err)
		return
	}

	m.streamsMu.Lock()
	m.streams[id] = localConn
	m.streamsMu.Unlock()

	defer func() {
		m.streamsMu.Lock()
		delete(m.streams, id)
		m.streamsMu.Unlock()
		closePayload := make([]byte, 4)
		binary.BigEndian.PutUint32(closePayload, id)
		m.writeFrame(protocol.TypeProxyClose, closePayload) //nolint:errcheck
	}()

	// local TCP → server TLS
	buf := make([]byte, 32768)
	for {
		n, err := localConn.Read(buf)
		if n > 0 {
			data := make([]byte, 4+n)
			binary.BigEndian.PutUint32(data[:4], id)
			copy(data[4:], buf[:n])
			if werr := m.writeFrame(protocol.TypeProxyData, data); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// ConnectProxy dials the server in proxy mode and starts local TCP listeners
// for each forward rule. It blocks until the tunnel is torn down.
func ConnectProxy(opts Options, forwards []ForwardRule) error {
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: opts.Insecure, //nolint:gosec
	}

	log.Printf("connecting to %s (proxy mode) ...", opts.ServerAddr)
	conn, err := tls.Dial("tcp", opts.ServerAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("dial %s: %w", opts.ServerAddr, err)
	}
	defer conn.Close()

	authPayload := protocol.Encode(protocol.AuthRequest{Token: opts.Token, Mode: "proxy"})
	if err := tunnel.WriteFrame(conn, protocol.TypeAuth, authPayload); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	msgType, payload, err := tunnel.ReadFrame(conn)
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	switch msgType {
	case protocol.TypeAuthFail:
		var fail protocol.AuthFailResponse
		_ = protocol.Decode(payload, &fail)
		return fmt.Errorf("authentication failed: %s", fail.Reason)
	case protocol.TypeAuthOK:
		// ok
	default:
		return fmt.Errorf("unexpected response type: %x", msgType)
	}

	log.Printf("authenticated — proxy mode active")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := newProxyMux(conn, ctx, cancel)

	// Start one listener per forward rule.
	listeners := make([]net.Listener, 0, len(forwards))
	for _, rule := range forwards {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", rule.LocalPort))
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return fmt.Errorf("listen on 127.0.0.1:%d: %w", rule.LocalPort, err)
		}
		listeners = append(listeners, ln)
		log.Printf("forwarding 127.0.0.1:%d → %s:%d", rule.LocalPort, rule.RemoteHost, rule.RemotePort)

		go func(ln net.Listener, rule ForwardRule) {
			defer ln.Close()
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go mux.handleLocalConn(c, rule)
			}
		}(ln, rule)
	}
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()

	if opts.Background {
		meta := TunnelMeta{
			ServerAddr:  opts.ServerAddr,
			ConnectedAt: time.Now().UTC(),
		}
		if err := WriteMeta(meta); err != nil {
			log.Printf("warning: write tunnel state: %v", err)
		} else {
			defer ClearMeta() //nolint:errcheck
		}
	} else {
		fmt.Printf("\n✓ Connected to auth-vpn (proxy mode)\n")
		for _, rule := range forwards {
			fmt.Printf("  127.0.0.1:%-5d → %s:%d\n", rule.LocalPort, rule.RemoteHost, rule.RemotePort)
		}
		fmt.Println()
	}

	go mux.readLoop()

	// Keepalive
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				mux.writeFrame(protocol.TypePing, nil) //nolint:errcheck
			case <-ctx.Done():
				return
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, shutdownSignals...)
	defer signal.Stop(sig)

	select {
	case <-sig:
		if !opts.Background {
			fmt.Println("\n  disconnecting...")
		}
		mux.writeFrame(protocol.TypeDisconnect, nil) //nolint:errcheck
		return ErrCleanDisconnect
	case <-ctx.Done():
		if !opts.Background {
			fmt.Println("\n  tunnel closed")
		}
	}

	return nil
}

// ConnectProxyWithReconnect wraps ConnectProxy with exponential backoff reconnect.
func ConnectProxyWithReconnect(opts Options, forwards []ForwardRule) error {
	backoff := reconnectInitialBackoff
	for attempt := 1; ; attempt++ {
		err := ConnectProxy(opts, forwards)
		if err == nil || errors.Is(err, ErrCleanDisconnect) {
			return nil
		}
		log.Printf("proxy tunnel dropped (attempt %d): %v — retrying in %s", attempt, err, backoff)
		time.Sleep(backoff)
		backoff *= 2
		if backoff > reconnectMaxBackoff {
			backoff = reconnectMaxBackoff
		}
	}
}
