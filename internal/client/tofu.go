package client

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adishM98/auth-vpn/internal/tlsutil"
	"gopkg.in/yaml.v3"
)

type knownHostsFile struct {
	Hosts map[string]string `yaml:"hosts"` // "host:port" → fingerprint
}

func knownHostsPath() string { return filepath.Join(stateDir(), "known_hosts.yaml") }

func loadKnownHosts() knownHostsFile {
	data, err := os.ReadFile(knownHostsPath())
	if err != nil {
		return knownHostsFile{Hosts: make(map[string]string)}
	}
	var kh knownHostsFile
	_ = yaml.Unmarshal(data, &kh)
	if kh.Hosts == nil {
		kh.Hosts = make(map[string]string)
	}
	return kh
}

func saveKnownHosts(kh knownHostsFile) error {
	data, err := yaml.Marshal(kh)
	if err != nil {
		return err
	}
	return writePrivate(knownHostsPath(), data)
}

func fetchFingerprint(addr string) (string, error) {
	return tlsutil.FetchFingerprint(addr)
}

func pinnedTLSConfig(fp string) *tls.Config {
	return tlsutil.PinnedTLSConfig(fp)
}

func isCertError(err error) bool {
	s := err.Error()
	return strings.Contains(s, "x509") ||
		strings.Contains(s, "certificate") ||
		strings.Contains(s, "tls: failed to verify")
}

// IsInteractive reports whether stdin is a terminal a person can answer on.
// /dev/null is also a character device, so it is ruled out explicitly: a
// service started with stdin from /dev/null must not wait on a prompt.
func IsInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(fi, null) {
		return false
	}
	return true
}

// DialTLS connects to addr with TOFU certificate pinning.
//
// Flow:
//  1. Known fingerprint in ~/.auth-vpn/known_hosts.yaml → pin verification (no CA chain).
//  2. No known fingerprint → try full CA verification.
//  3. CA fails with a cert error → prompt user (or auto-trust in non-interactive mode).
//  4. Fingerprint saved; subsequent connections skip the prompt entirely.
//
// A fingerprint given up front (--fingerprint) is enforced before anything
// else, so CI can pin the server without a known_hosts file or --insecure.
func DialTLS(addr string, insecure bool, fingerprint string) (*tls.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}

	if fingerprint != "" {
		return tls.DialWithDialer(dialer, "tcp", addr, pinnedTLSConfig(fingerprint))
	}
	if insecure {
		return tls.DialWithDialer(dialer, "tcp", addr,
			&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) //nolint:gosec
	}

	kh := loadKnownHosts()

	// Known fingerprint — verify against pin, no CA chain needed.
	if fp, ok := kh.Hosts[addr]; ok {
		return tls.DialWithDialer(dialer, "tcp", addr, pinnedTLSConfig(fp))
	}

	// Try full CA verification first (works for properly-signed certs).
	conn, err := tls.DialWithDialer(dialer, "tcp", addr,
		&tls.Config{MinVersion: tls.VersionTLS13})
	if err == nil {
		return conn, nil
	}

	// Non-cert errors (network failure, wrong port, etc.) — surface as-is.
	if !isCertError(err) {
		return nil, err
	}

	// Self-signed or untrusted CA — fetch fingerprint and ask user.
	fp, fetchErr := fetchFingerprint(addr)
	if fetchErr != nil {
		return nil, fmt.Errorf("TLS: %w", err)
	}

	if IsInteractive() {
		fmt.Printf("\n⚠  Server is using a self-signed certificate.\n")
		fmt.Printf("   Server      : %s\n", addr)
		fmt.Printf("   Fingerprint : %s\n\n", fp)
		fmt.Printf("   Trust this server? [y/N] ")

		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if a := strings.TrimSpace(strings.ToLower(answer)); a != "y" && a != "yes" {
			return nil, fmt.Errorf("certificate not trusted")
		}
	} else {
		// Non-interactive (CI, background daemon) — auto-trust and log.
		log.Printf("server has self-signed certificate — auto-trusting (fingerprint: %s)", fp)
	}

	kh.Hosts[addr] = fp
	if saveErr := saveKnownHosts(kh); saveErr != nil {
		log.Printf("warning: could not save server fingerprint: %v", saveErr)
	} else if IsInteractive() {
		fmt.Printf("   ✓ Fingerprint saved — won't ask again\n\n")
	}

	return tls.DialWithDialer(dialer, "tcp", addr, pinnedTLSConfig(fp))
}
