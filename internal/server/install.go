package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/adishM98/auth-vpn/internal/auth"
)

const (
	ConfigDir      = "/etc/auth-vpn"
	TLSDir         = "/etc/auth-vpn/tls"
	CertFile       = "/etc/auth-vpn/tls/cert.pem"
	KeyFile        = "/etc/auth-vpn/tls/key.pem"
	TokensFile     = "/etc/auth-vpn/tokens.yaml"
	TokenPepperFile = "/etc/auth-vpn/token_pepper" // server-secret HMAC pepper for token hashing
	WhitelistFile  = "/etc/auth-vpn/whitelist.json"
	ForwardsFile   = "/etc/auth-vpn/forwards.json"
	SSHKeysFile    = "/etc/auth-vpn/ssh_keys.json"
	ServiceFile    = "/etc/systemd/system/auth-vpn.service"
	SocketFile     = "/var/run/auth-vpn.sock"
)

// Install sets up the server: TLS cert, initial token, server.yaml, acl.yaml, systemd service.
// Returns the public IP, raw initial token, and generated API key.
func Install(port int) (publicIP, rawToken, apiKey string, err error) {
	for _, d := range []string{ConfigDir, TLSDir} {
		if err = os.MkdirAll(d, 0o700); err != nil {
			return "", "", "", fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	publicIP, err = detectPublicIP()
	if err != nil {
		publicIP = "<your-vm-ip>"
	}

	// Only generate a TLS cert on a fresh install — skip if one already exists
	// so that updates don't invalidate existing client connections.
	if _, statErr := os.Stat(CertFile); os.IsNotExist(statErr) {
		if err = generateSelfSignedCert(publicIP, CertFile, KeyFile); err != nil {
			return "", "", "", fmt.Errorf("generate TLS cert: %w", err)
		}
	}

	// Generate a token pepper on fresh install (never overwrites existing one).
	if _, statErr := os.Stat(TokenPepperFile); os.IsNotExist(statErr) {
		if pepperErr := generateTokenPepper(TokenPepperFile); pepperErr != nil {
			fmt.Fprintf(os.Stderr, "warning: generate token pepper: %v\n", pepperErr)
		}
	}

	pepper, _ := LoadTokenPepper(TokenPepperFile)
	tm, err := auth.NewManager(TokensFile, pepper)
	if err != nil {
		return "", "", "", fmt.Errorf("token manager: %w", err)
	}
	// Only create the initial token on a truly fresh install.
	// If tokens.yaml already exists with an "admin" token, don't add a duplicate.
	hasAdmin := false
	for _, t := range tm.List() {
		if t.Name == "admin" {
			hasAdmin = true
			break
		}
	}
	if !hasAdmin {
		rawToken, err = tm.Add("admin", nil, false)
		if err != nil {
			return "", "", "", fmt.Errorf("create initial token: %w", err)
		}
	} else {
		rawToken = "<existing — run: auth-vpn server tokens list>"
	}

	// Load existing server.yaml if present so custom settings (forward_bind_addr,
	// subnet, api_key, etc.) are preserved across updates.
	var cfg ServerConfig
	if existingCfg, loadErr := LoadServerConfig(ServerConfigFile); loadErr == nil {
		cfg = existingCfg
		// Update port only if a non-zero value was explicitly requested.
		if port != 0 {
			cfg.Port = port
		}
		// Re-use the existing API key so the dashboard stays accessible after update.
		if cfg.APIKey != "" {
			apiKey = cfg.APIKey
		} else {
			if apiKey, err = generateAPIKey(); err != nil {
				return "", "", "", fmt.Errorf("generate API key: %w", err)
			}
			cfg.APIKey = apiKey
		}
	} else {
		// Fresh install — start from defaults.
		if apiKey, err = generateAPIKey(); err != nil {
			return "", "", "", fmt.Errorf("generate API key: %w", err)
		}
		cfg = DefaultServerConfig(port)
		cfg.APIKey = apiKey
	}

	if err = SaveServerConfig(ServerConfigFile, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write server.yaml: %v\n", err)
	}

	if err = writeDefaultACL(ACLFile); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write acl.yaml: %v\n", err)
	}

	if err = WriteSystemdService(port); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		err = nil
	}

	// Create dedicated system user if not present and chown config directory.
	ensureServiceUser()

	return publicIP, rawToken, apiKey, nil
}

// ensureServiceUser creates the auth-vpn system user/group if they don't exist
// and sets ownership of /etc/auth-vpn so the service can read its config.
func ensureServiceUser() {
	// Only attempt on Linux where useradd/groupadd are standard.
	if _, err := exec.LookPath("useradd"); err != nil {
		return
	}
	// Create group (ignore error if already exists).
	exec.Command("groupadd", "--system", "auth-vpn").Run() //nolint:errcheck
	// Create user (ignore error if already exists).
	exec.Command("useradd", "--system", "--no-create-home",
		"--shell", "/usr/sbin/nologin",
		"--gid", "auth-vpn",
		"auth-vpn").Run() //nolint:errcheck
	// Transfer ownership of config directory.
	exec.Command("chown", "-R", "auth-vpn:auth-vpn", ConfigDir).Run() //nolint:errcheck
	// The audit log dir must also be writable by the service user.
	exec.Command("install", "-d", "-o", "auth-vpn", "-g", "auth-vpn", "-m", "0750", "/var/log").Run() //nolint:errcheck
}

// generateTokenPepper creates a 32-byte random pepper and writes it to path.
func generateTokenPepper(path string) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// LoadTokenPepper reads the raw pepper bytes from path.
// Returns nil bytes (no error) when the file does not exist — pepper is optional.
func LoadTokenPepper(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

func generateAPIKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeDefaultACL(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // don't overwrite existing acl.yaml
	}
	content := `# auth-vpn ACL rules
# default_policy: allow | deny
default_policy: allow

# rules:
#   - device: device-name
#     allow:
#       - proto: tcp
#         port: 5432
#     deny: []
rules: []
`
	return os.WriteFile(path, []byte(content), 0o600)
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
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
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

func WriteSystemdService(port int) error {
	exe, _ := os.Executable()
	svc := fmt.Sprintf(`[Unit]
Description=auth-vpn tunnel server
After=network.target

[Service]
ExecStart=%s server start --port %d
Restart=always
RestartSec=5

# Run as a dedicated non-root user with only the capabilities required for TUN.
User=auth-vpn
Group=auth-vpn
# CAP_NET_ADMIN: create/configure TUN interfaces.
# CAP_NET_BIND_SERVICE: bind privileged ports if needed.
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_BIND_SERVICE

# Harden the service environment.
NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ReadWritePaths=/etc/auth-vpn /var/run /var/log
ProtectHome=yes
RestrictNamespaces=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
`, exe, port)
	return os.WriteFile(ServiceFile, []byte(svc), 0o644)
}
