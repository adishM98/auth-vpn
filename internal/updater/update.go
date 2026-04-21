package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	apiURL  = "https://api.github.com/repos/adishM98/auth-vpn/releases/latest"
	baseURL = "https://github.com/adishM98/auth-vpn/releases/download"
)

type githubRelease struct {
	TagName string `json:"tag_name"` // e.g. "v1.0.3"
}

// LatestVersion fetches the latest release tag from GitHub.
func LatestVersion() (string, error) {
	c := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, apiURL, nil)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch release info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("decode release info: %w", err)
	}
	return rel.TagName, nil
}

// binaryName returns the asset name for the current platform.
func binaryName() (string, error) {
	os_, arch := runtime.GOOS, runtime.GOARCH
	switch {
	case os_ == "linux" && arch == "amd64":
		return "auth-vpn-linux-amd64", nil
	case os_ == "darwin" && arch == "arm64":
		return "auth-vpn-darwin-arm64", nil
	case os_ == "darwin" && arch == "amd64":
		return "auth-vpn-darwin-amd64", nil
	case os_ == "windows" && arch == "amd64":
		return "auth-vpn-windows-amd64.exe", nil
	default:
		return "", fmt.Errorf("no pre-built binary for %s/%s — build from source", os_, arch)
	}
}

// Run downloads the latest release and atomically replaces the running binary.
// currentVersion should be the version string embedded at build time (e.g. "v1.0.2").
func Run(currentVersion string) error {
	fmt.Printf("current version : %s\n", currentVersion)
	fmt.Printf("checking latest  ...\n")

	latest, err := LatestVersion()
	if err != nil {
		return err
	}
	fmt.Printf("latest version  : %s\n", latest)

	if strings.TrimPrefix(latest, "v") == strings.TrimPrefix(currentVersion, "v") {
		fmt.Println("already up to date.")
		return nil
	}

	asset, err := binaryName()
	if err != nil {
		return err
	}

	downloadURL := fmt.Sprintf("%s/%s/%s", baseURL, latest, asset)
	fmt.Printf("downloading      : %s\n", downloadURL)

	// Find the path of the running binary.
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate current binary: %w", err)
	}

	// Download to a temp file alongside the current binary.
	tmpPath := exePath + ".new"
	if err := downloadFile(downloadURL, tmpPath); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return err
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("chmod: %w", err)
	}

	// Atomic replace.
	if err := os.Rename(tmpPath, exePath); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("replace binary (try with sudo): %w", err)
	}

	migrateServerConfig()
	restartService()
	fmt.Printf("updated to %s ✓\n", latest)
	return nil
}

// serverConfigPath is the well-known location for the server config.
const serverConfigPath = "/etc/auth-vpn/server.yaml"

// serverConfigYAML is a minimal representation of server.yaml for migration purposes.
type serverConfigYAML struct {
	ForwardBindAddr string `yaml:"forward_bind_addr,omitempty"`
}

// migrateServerConfig auto-populates forward_bind_addr in server.yaml if missing.
// Called after a binary update so the DevOps operator doesn't need to edit config manually.
func migrateServerConfig() {
	if runtime.GOOS != "linux" {
		return
	}

	data, err := os.ReadFile(serverConfigPath)
	if err != nil {
		return // not a server install, skip silently
	}

	var cfg serverConfigYAML
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return
	}
	if cfg.ForwardBindAddr != "" {
		return // already set
	}

	ip := outboundIP()
	if ip == "" {
		fmt.Println("warning: could not detect public IP for forward_bind_addr — set it manually in /etc/auth-vpn/server.yaml")
		return
	}

	// Append the new key to the existing YAML rather than re-marshalling the
	// whole file, so we don't lose any fields we don't know about.
	line := fmt.Sprintf("\nforward_bind_addr: %s\n", ip)
	f, err := os.OpenFile(serverConfigPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Printf("warning: update server.yaml: %v\n", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		fmt.Printf("warning: update server.yaml: %v\n", err)
		return
	}
	fmt.Printf("config migrated  : forward_bind_addr = %s\n", ip)
}

// outboundIP returns the local IP used for outbound traffic by probing a UDP
// address. No data is actually sent.
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// restartService restarts the auth-vpn systemd unit if it is currently active.
func restartService() {
	if runtime.GOOS != "linux" {
		return
	}
	if err := exec.Command("systemctl", "is-active", "--quiet", "auth-vpn").Run(); err != nil {
		return // service not running, nothing to restart
	}
	fmt.Print("restarting service ...")
	if err := exec.Command("systemctl", "restart", "auth-vpn").Run(); err != nil {
		fmt.Printf("\nwarning: restart failed — run: sudo systemctl restart auth-vpn\n")
		return
	}
	fmt.Println(" done")
}

func downloadFile(url, dest string) error {
	c := &http.Client{Timeout: 5 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write download: %w", err)
	}
	return nil
}
