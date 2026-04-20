package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/adishM98/auth-vpn/internal/tunnel"
	"github.com/adishM98/auth-vpn/pkg/protocol"
)

// ErrCleanDisconnect is returned by Connect when the tunnel was torn down
// intentionally (SIGTERM or server TypeDisconnect). ConnectWithReconnect
// treats this as a normal exit and does not retry.
var ErrCleanDisconnect = errors.New("clean disconnect")

const tunBufSize = 65535

// Options controls how the client connects.
type Options struct {
	ServerAddr string // "host:port"
	Token      string
	Background bool // suppress interactive output, write PID state file
	Wait       bool // block until tunnel verified before returning
	Insecure   bool // skip TLS cert verification (dev only)
	Reconnect  bool // auto-reconnect with exponential backoff on unexpected drop
}

// Profile is a saved connection profile (stored in ~/.auth-vpn/profiles.yaml).
type Profile struct {
	Name   string `yaml:"name"`
	Host   string `yaml:"host"`
	Token  string `yaml:"token"`
}

// Connect dials the server, authenticates, sets up the TUN interface,
// then forwards all traffic. Blocks until the tunnel is torn down.
func Connect(opts Options) error {
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: opts.Insecure, //nolint:gosec // user opt-in for dev
	}

	log.Printf("connecting to %s ...", opts.ServerAddr)
	conn, err := tls.Dial("tcp", opts.ServerAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("dial %s: %w", opts.ServerAddr, err)
	}

	// Send AUTH frame.
	authPayload := protocol.Encode(protocol.AuthRequest{Token: opts.Token})
	if err := tunnel.WriteFrame(conn, protocol.TypeAuth, authPayload); err != nil {
		conn.Close()
		return fmt.Errorf("send auth: %w", err)
	}

	// Read response.
	msgType, payload, err := tunnel.ReadFrame(conn)
	if err != nil {
		conn.Close()
		return fmt.Errorf("read auth response: %w", err)
	}
	switch msgType {
	case protocol.TypeAuthFail:
		var fail protocol.AuthFailResponse
		_ = protocol.Decode(payload, &fail)
		conn.Close()
		return fmt.Errorf("authentication failed: %s", fail.Reason)
	case protocol.TypeAuthOK:
		// proceed
	default:
		conn.Close()
		return fmt.Errorf("unexpected response type: %x", msgType)
	}

	var resp protocol.AuthOKResponse
	if err := protocol.Decode(payload, &resp); err != nil {
		conn.Close()
		return fmt.Errorf("decode auth response: %w", err)
	}

	log.Printf("authenticated — assigned IP: %s", resp.ClientIP)

	// Create TUN interface.
	iface, err := tunnel.NewTUN(resp.ClientIP + "/24")
	if err != nil {
		conn.Close()
		return fmt.Errorf("create client tun: %w", err)
	}
	defer iface.Close()

	ifaceName := iface.Name()

	// Platform-specific: on macOS we need point-to-point config.
	if err := configureTUN(ifaceName, resp.ClientIP, resp.ServerIP); err != nil {
		conn.Close()
		return fmt.Errorf("configure tun: %w", err)
	}

	// Add OS route so all traffic to the subnet goes through the TUN.
	if err := tunnel.AddRoute(resp.Subnet, ifaceName); err != nil {
		log.Printf("warning: add route: %v", err)
	}
	defer tunnel.DelRoute(resp.Subnet) //nolint:errcheck

	log.Printf("tunnel up — route %s via %s", resp.Subnet, ifaceName)

	if opts.Background {
		meta := TunnelMeta{
			ServerAddr:  opts.ServerAddr,
			AssignedIP:  resp.ClientIP,
			ServerIP:    resp.ServerIP,
			Subnet:      resp.Subnet,
			ConnectedAt: time.Now().UTC(),
		}
		if err := WriteMeta(meta); err != nil {
			log.Printf("warning: could not write tunnel state: %v", err)
		} else {
			defer ClearMeta() //nolint:errcheck
		}
	} else {
		fmt.Printf("\n✓ Connected to auth-vpn\n")
		fmt.Printf("  Tunnel IP : %s\n", resp.ClientIP)
		fmt.Printf("  Server IP : %s\n", resp.ServerIP)
		fmt.Printf("  Subnet    : %s\n\n", resp.Subnet)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// TUN → server
	go func() {
		defer cancel()
		buf := make([]byte, tunBufSize)
		for {
			n, err := iface.Read(buf)
			if err != nil {
				return
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			if err := tunnel.WriteFrame(conn, protocol.TypeIPPacket, pkt); err != nil {
				return
			}
		}
	}()

	// Server → TUN
	go func() {
		defer cancel()
		for {
			msgType, payload, err := tunnel.ReadFrame(conn)
			if err != nil {
				return
			}
			switch msgType {
			case protocol.TypeIPPacket:
				if _, err := iface.Write(payload); err != nil {
					return
				}
			case protocol.TypePing:
				_ = tunnel.WriteFrame(conn, protocol.TypePong, nil)
			case protocol.TypeDisconnect:
				return
			}
		}
	}()

	// Keepalive ping every 30s.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = tunnel.WriteFrame(conn, protocol.TypePing, nil)
			case <-ctx.Done():
				return
			}
		}
	}()

	// Handle OS signals for graceful disconnect.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, shutdownSignals...)
	defer signal.Stop(sig)

	select {
	case <-sig:
		if !opts.Background {
			fmt.Println("\n  disconnecting...")
		}
		_ = tunnel.WriteFrame(conn, protocol.TypeDisconnect, nil)
		conn.Close()
		return ErrCleanDisconnect
	case <-ctx.Done():
		if !opts.Background {
			fmt.Println("\n  tunnel closed")
		}
	}

	conn.Close()
	return nil
}

const (
	reconnectInitialBackoff = 2 * time.Second
	reconnectMaxBackoff     = 2 * time.Minute
)

// ConnectWithReconnect calls Connect in a loop with exponential backoff,
// retrying on unexpected tunnel drops. A clean disconnect (SIGTERM or server
// TypeDisconnect) breaks the loop and returns nil.
func ConnectWithReconnect(opts Options) error {
	backoff := reconnectInitialBackoff
	for attempt := 1; ; attempt++ {
		err := Connect(opts)
		if err == nil || errors.Is(err, ErrCleanDisconnect) {
			return nil
		}
		log.Printf("tunnel dropped (attempt %d): %v — retrying in %s", attempt, err, backoff)
		time.Sleep(backoff)
		backoff *= 2
		if backoff > reconnectMaxBackoff {
			backoff = reconnectMaxBackoff
		}
	}
}

// WaitForPing dials the server and returns once a TCP connection succeeds.
// Used with --wait flag in CI to ensure the tunnel is up before continuing.
func WaitForPing(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("server not reachable within %s", timeout)
}
