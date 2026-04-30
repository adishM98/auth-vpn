package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshHostKeyFile = "/etc/auth-vpn/ssh_host_key"

// startSSHServer runs an embedded SSH server for token-authenticated port forwarding.
// Any SSH client connects with any username + auth-vpn token as password,
// then uses standard SSH local port forwarding to reach services on this host.
func (s *Server) startSSHServer() {
	if s.cfg.SSHAddr == "" {
		return
	}

	hostKey, err := loadOrGenerateSSHHostKey(sshHostKeyFile)
	if err != nil {
		log.Printf("SSH server: host key: %v", err)
		return
	}

	cfg := &ssh.ServerConfig{
		// Password auth: any username, auth-vpn token as password.
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			remoteIP, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			tok, err := s.tokens.Validate(string(pass))
			if err != nil {
				s.limiter.RecordFailure(remoteIP)
				s.metrics.IncAuthFailure()
				return nil, fmt.Errorf("invalid token")
			}
			s.limiter.Reset(remoteIP)
			return &ssh.Permissions{
				Extensions: map[string]string{"name": tok.Name},
			}, nil
		},
		// Public key auth: keys registered via /api/ssh-keys OR present in the
		// system authorized_keys for the connecting user.
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			remoteIP, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			// Check auth-vpn managed keys first.
			if name, ok := s.sshKeys.FindKey(key); ok {
				s.limiter.Reset(remoteIP)
				return &ssh.Permissions{
					Extensions: map[string]string{"name": name},
				}, nil
			}
			// Fall back to system authorized_keys — any key that can SSH into
			// this host as the given user is trusted for tunneling too.
			if checkSystemAuthorizedKeys(key, c.User()) {
				s.limiter.Reset(remoteIP)
				return &ssh.Permissions{
					Extensions: map[string]string{"name": c.User()},
				}, nil
			}
			s.limiter.RecordFailure(remoteIP)
			s.metrics.IncAuthFailure()
			return nil, fmt.Errorf("unauthorized key")
		},
	}
	cfg.AddHostKey(hostKey)

	ln, err := net.Listen("tcp", s.cfg.SSHAddr)
	if err != nil {
		log.Printf("SSH server: listen %s: %v", s.cfg.SSHAddr, err)
		return
	}

	go func() {
		<-s.done
		ln.Close()
	}()

	log.Printf("SSH server listening on %s (token-password auth)", s.cfg.SSHAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleSSHConn(conn, cfg)
	}
}

func (s *Server) handleSSHConn(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		log.Printf("SSH: handshake from %s: %v", conn.RemoteAddr(), err)
		return
	}
	defer sshConn.Close()

	name := sshConn.Permissions.Extensions["name"]
	log.Printf("SSH: %s connected from %s", name, conn.RemoteAddr())

	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "direct-tcpip" {
			newChan.Reject(ssh.UnknownChannelType, "only direct-tcpip forwarding supported")
			continue
		}
		go s.handleSSHTunnel(newChan, name)
	}

	log.Printf("SSH: %s disconnected", name)
}

// directTCPIPData is the wire format for SSH direct-tcpip channel extra data (RFC 4254 §7.2).
type directTCPIPData struct {
	DestHost   string
	DestPort   uint32
	OriginHost string
	OriginPort uint32
}

func (s *Server) handleSSHTunnel(newChan ssh.NewChannel, clientName string) {
	var req directTCPIPData
	if err := ssh.Unmarshal(newChan.ExtraData(), &req); err != nil {
		newChan.Reject(ssh.ConnectionFailed, "malformed request")
		return
	}

	target := net.JoinHostPort(req.DestHost, fmt.Sprintf("%d", req.DestPort))

	if !isAllowedSSHTarget(req.DestHost) {
		log.Printf("SSH: %s → %s rejected (not a local target)", clientName, target)
		newChan.Reject(ssh.ConnectionFailed, "target not permitted: only localhost and VPN subnet allowed")
		return
	}

	backend, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		log.Printf("SSH: %s → %s: dial error: %v", clientName, target, err)
		newChan.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	defer backend.Close()

	ch, reqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)

	log.Printf("SSH tunnel: %s → %s", clientName, target)

	done := make(chan struct{}, 2)
	go func() { io.Copy(backend, ch); done <- struct{}{} }() //nolint:errcheck
	go func() { io.Copy(ch, backend); done <- struct{}{} }() //nolint:errcheck
	<-done
}

// checkSystemAuthorizedKeys returns true if key appears in the system
// authorized_keys for the given username. This lets any key that can SSH into
// the host also use auth-vpn's embedded SSH server without manual registration.
func checkSystemAuthorizedKeys(key ssh.PublicKey, username string) bool {
	// Reject usernames that could escape the /home/<user>/ directory.
	if !isSafeUsername(username) {
		return false
	}
	fp := ssh.FingerprintSHA256(key)
	candidates := []string{
		fmt.Sprintf("/home/%s/.ssh/authorized_keys", username),
		"/root/.ssh/authorized_keys",
		"/etc/auth-vpn/authorized_keys", // optional dedicated file
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rest := data
		for len(rest) > 0 {
			parsed, _, _, remaining, err := ssh.ParseAuthorizedKey(rest)
			rest = remaining
			if err != nil {
				break
			}
			if ssh.FingerprintSHA256(parsed) == fp {
				return true
			}
		}
	}
	return false
}

// isAllowedSSHTarget permits forwarding only to localhost and VPN subnet,
// preventing SSRF to external services.
func isAllowedSSHTarget(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return localhostNet.Contains(ip) || vpnSubnet.Contains(ip)
}

// isSafeUsername returns true if username is safe to interpolate into a file path.
// Rejects empty strings and any username containing path separators or dots that
// could escape the /home/<user>/ directory.
func isSafeUsername(u string) bool {
	return u != "" && !strings.Contains(u, "/") && !strings.Contains(u, ".")
}

// loadOrGenerateSSHHostKey loads the RSA host key from path, generating and
// saving a new one if it doesn't exist yet.
func loadOrGenerateSSHHostKey(path string) (ssh.Signer, error) {
	if data, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(data)
		if block != nil {
			if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
				return ssh.NewSignerFromKey(key)
			}
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}

	pemData := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, pemData, 0o600); err != nil {
		log.Printf("SSH: warning: could not save host key to %s: %v", path, err)
	}

	return ssh.NewSignerFromKey(key)
}
