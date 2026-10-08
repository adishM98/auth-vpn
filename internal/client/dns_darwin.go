//go:build darwin

package client

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

const resolverMarker = "# managed by auth-vpn — removed on disconnect\n"

// applyDNS writes one /etc/resolver/<domain> file per domain so macOS sends
// those lookups to server. Files auth-vpn didn't create are left alone.
// Returns a func that removes what was written.
func applyDNS(_ string, server string, domains []string) func() {
	if err := os.MkdirAll("/etc/resolver", 0o755); err != nil {
		log.Printf("warning: pushed DNS skipped: %v", err)
		return func() {}
	}
	var written []string
	for _, d := range domains {
		path := filepath.Join("/etc/resolver", d)
		if b, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(b), resolverMarker) {
			log.Printf("warning: %s exists and isn't managed by auth-vpn — leaving it", path)
			continue
		}
		if err := os.WriteFile(path, []byte(resolverMarker+"nameserver "+server+"\n"), 0o644); err != nil {
			log.Printf("warning: write %s: %v", path, err)
			continue
		}
		written = append(written, path)
	}
	return func() {
		for _, p := range written {
			_ = os.Remove(p)
		}
	}
}
