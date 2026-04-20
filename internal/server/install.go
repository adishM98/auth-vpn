package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adishM98/auth-vpn/internal/auth"
)

const (
	ConfigDir   = "/etc/auth-vpn"
	TLSDir      = "/etc/auth-vpn/tls"
	CertFile    = "/etc/auth-vpn/tls/cert.pem"
	KeyFile     = "/etc/auth-vpn/tls/key.pem"
	TokensFile  = "/etc/auth-vpn/tokens.yaml"
	ServiceFile = "/etc/systemd/system/auth-vpn.service"
)

// Install sets up the server: TLS cert, initial token, systemd service.
// Returns the public IP and raw initial token.
func Install(port int) (publicIP, rawToken string, err error) {
	for _, d := range []string{ConfigDir, TLSDir} {
		if err = os.MkdirAll(d, 0o700); err != nil {
			return "", "", fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	publicIP, err = detectPublicIP()
	if err != nil {
		publicIP = "<your-vm-ip>"
	}

	if err = generateSelfSignedCert(publicIP, CertFile, KeyFile); err != nil {
		return "", "", fmt.Errorf("generate TLS cert: %w", err)
	}

	tm, err := auth.NewManager(TokensFile)
	if err != nil {
		return "", "", fmt.Errorf("token manager: %w", err)
	}
	rawToken, err = tm.Add("admin", nil, false)
	if err != nil {
		return "", "", fmt.Errorf("create initial token: %w", err)
	}

	if err = writeSystemdService(port); err != nil {
		// non-fatal
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		err = nil
	}

	return publicIP, rawToken, nil
}

func detectPublicIP() (string, error) {
	endpoints := []string{
		"https://api.ipify.org",
		"https://checkip.amazonaws.com",
		"https://icanhazip.com",
	}
	c := &http.Client{Timeout: 5 * time.Second}
	for _, ep := range endpoints {
		resp, err := c.Get(ep)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		ip := strings.TrimSpace(string(body))
		if net.ParseIP(ip) != nil {
			return ip, nil
		}
	}
	return "", fmt.Errorf("could not detect public IP")
}

func generateSelfSignedCert(host, certPath, keyPath string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"auth-vpn"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return err
	}
	cf, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer cf.Close()
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	kf, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer kf.Close()
	return pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func writeSystemdService(port int) error {
	exe, _ := os.Executable()
	svc := fmt.Sprintf(`[Unit]
Description=auth-vpn tunnel server
After=network.target

[Service]
ExecStart=%s server start --port %d
Restart=always
RestartSec=5
User=root

[Install]
WantedBy=multi-user.target
`, exe, port)
	return os.WriteFile(ServiceFile, []byte(svc), 0o644)
}
