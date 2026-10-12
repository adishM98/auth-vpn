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
	if s.cfg.SSHAddr == "" || s.cfg.SSHAddr == "off" {
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
			return &ssh.Permissions{
				Extensions: map[string]string{"name": tok.Name, "via": viaToken},
			}, nil
		},
		// Public key auth: keys registered via /api/ssh-keys OR present in the
		// system authorized_keys for the connecting user.
		// The limiter is reset only after a full handshake (handleSSHConn): this
		// callback also runs for signature-less key queries, which prove nothing.
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			remoteIP, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			// Check auth-vpn managed keys first.
			if name, ok := s.sshKeys.FindKey(key); ok {
				return &ssh.Permissions{
					Extensions: map[string]string{"name": name, "via": viaSSHKey},
				}, nil
			}
			// Fall back to system authorized_keys — any key that can SSH into
			// this host as the given user, without forwarding restrictions,
			// is trusted for tunneling too.
			if checkSystemAuthorizedKeys(key, c.User()) {
				return &ssh.Permissions{
					Extensions: map[string]string{"name": c.User(), "via": viaSystemKey},
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

	acceptLoop(ln, s.sshPreAuth, s.done, func(conn net.Conn, release func()) {
		s.handleSSHConn(conn, cfg, release)
	})
}

func (s *Server) handleSSHConn(conn net.Conn, cfg *ssh.ServerConfig, release func()) {
	defer conn.Close()
	defer release()
	remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	if s.limiter.IsBanned(remoteIP) {
		return
	}

	// Version exchange, key exchange and auth must finish within the deadline.
	_ = conn.SetDeadline(time.Now().Add(preAuthTimeout))
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		log.Printf("SSH: handshake from %s: %v", conn.RemoteAddr(), err)
		return
	}
	defer sshConn.Close()
	_ = conn.SetDeadline(time.Time{})
	release()
	s.limiter.Reset(remoteIP)

	name := sshConn.Permissions.Extensions["name"]
	defer s.sessions.add(&session{name: name, via: sshConn.Permissions.Extensions["via"], mode: "ssh", remote: remoteIP, conn: sshConn})()
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

	if !s.isLocalTarget(req.DestHost) {
		log.Printf("SSH: %s → %s rejected (not a local target)", clientName, target)
		newChan.Reject(ssh.ConnectionFailed, "target not permitted: only localhost and VPN subnet allowed")
		return
	}
	if s.acl != nil && !s.acl.AllowPort(clientName, "tcp", int(req.DestPort)) {
		log.Printf("SSH: %s → %s rejected by ACL", clientName, target)
		newChan.Reject(ssh.Prohibited, "denied by ACL")
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
	return authorizedKeysMatch(key,
		fmt.Sprintf("/home/%s/.ssh/authorized_keys", username),
		"/root/.ssh/authorized_keys",
		"/etc/auth-vpn/authorized_keys", // optional dedicated file
	)
}

// restrictingOptions are authorized_keys options that limit what a key may
// do under sshd. auth-vpn can't honour them, so such keys are not accepted
// here at all; register the key through /api/ssh-keys instead.
var restrictingOptions = []string{"restrict", "no-port-forwarding", "command=", "permitopen=", "permitlisten=", "from=", "expiry-time=", "principals="}

func authorizedKeysMatch(key ssh.PublicKey, candidates ...string) bool {
	fp := ssh.FingerprintSHA256(key)
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rest := data
		for len(rest) > 0 {
			parsed, _, opts, remaining, err := ssh.ParseAuthorizedKey(rest)
			rest = remaining
			if err != nil {
				break
			}
			if ssh.FingerprintSHA256(parsed) == fp && !restricted(opts) {
				return true
			}
		}
	}
	return false
}

func restricted(opts []string) bool {
	for _, o := range opts {
		for _, r := range restrictingOptions {
			if strings.EqualFold(o, r) || (strings.HasSuffix(r, "=") && strings.HasPrefix(strings.ToLower(o), r)) {
				return true
			}
		}
	}
	return false
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
