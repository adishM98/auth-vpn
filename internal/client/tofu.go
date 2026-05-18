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

func knownHostsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auth-vpn", "known_hosts.yaml")
}

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
	path := knownHostsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(kh)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
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

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// DialTLS connects to addr with TOFU certificate pinning.
//
// Flow:
//  1. Known fingerprint in ~/.auth-vpn/known_hosts.yaml → pin verification (no CA chain).
//  2. No known fingerprint → try full CA verification.
//  3. CA fails with a cert error → prompt user (or auto-trust in non-interactive mode).
//  4. Fingerprint saved; subsequent connections skip the prompt entirely.
func DialTLS(addr string, insecure bool) (*tls.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}

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

	if isInteractive() {
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
	} else if isInteractive() {
		fmt.Printf("   ✓ Fingerprint saved — won't ask again\n\n")
	}

	return tls.DialWithDialer(dialer, "tcp", addr, pinnedTLSConfig(fp))
}
