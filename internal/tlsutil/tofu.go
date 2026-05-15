package tlsutil

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net"
	"time"
)

// CertFingerprint returns the SHA256 fingerprint of a TLS certificate.
func CertFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "SHA256:" + base64.StdEncoding.EncodeToString(sum[:])
}

// FetchFingerprint dials addr (host:port) with TLS verification disabled
// solely to retrieve the server certificate fingerprint.
func FetchFingerprint(addr string) (string, error) {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp", addr,
		&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}, //nolint:gosec
	)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", fmt.Errorf("server sent no certificates")
	}
	return CertFingerprint(certs[0]), nil
}

// PinnedTLSConfig returns a TLS config that verifies the server presents
// a certificate matching fp instead of doing CA chain verification.
func PinnedTLSConfig(fp string) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec
		MinVersion:         tls.VersionTLS13,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("server sent no certificates")
			}
			got := CertFingerprint(cs.PeerCertificates[0])
			if got != fp {
				return fmt.Errorf(
					"certificate fingerprint mismatch\n  expected: %s\n  got: %s",
					fp, got,
				)
			}
			return nil
		},
	}
}
