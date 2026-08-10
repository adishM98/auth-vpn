package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	// localhostNet is the loopback range used for SSRF and probe-host checks.
	localhostNet = func() *net.IPNet { _, n, _ := net.ParseCIDR("127.0.0.0/8"); return n }()
)

// vpnSubnet is set at server startup from cfg.Subnet so the probe and SSH
// target checks always match the actual VPN range, not a hard-coded default.
// Guarded by vpnSubnetMu; safe to read/write from concurrent goroutines.
var (
	vpnSubnet   *net.IPNet
	vpnSubnetMu sync.RWMutex
)

func init() {
	// Fall back to the default subnet so tests and code that doesn't call
	// setVPNSubnet still get a valid (non-nil) *net.IPNet value.
	_, n, _ := net.ParseCIDR("10.8.0.0/24")
	vpnSubnet = n
}

// setVPNSubnet updates the package-level vpnSubnet from the running server's
// configuration. Called once from New() before any connections are accepted.
func setVPNSubnet(cidr string) {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil || n == nil {
		return
	}
	vpnSubnetMu.Lock()
	vpnSubnet = n
	vpnSubnetMu.Unlock()
}

// getVPNSubnet returns the current VPN subnet safely.
func getVPNSubnet() *net.IPNet {
	vpnSubnetMu.RLock()
	defer vpnSubnetMu.RUnlock()
	return vpnSubnet
}

// isAllowedProbeHost restricts /plugin/probe to VPN-internal IPs and localhost only,
// preventing SSRF to cloud metadata endpoints or arbitrary internet hosts.
func isAllowedProbeHost(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return getVPNSubnet().Contains(ip) || localhostNet.Contains(ip)
}

// handlePlugin serves the /plugin/* routes used by app integrations.
// Routes:
//
//	GET  /plugin/status          — server health + active client count
//	GET  /plugin/clients         — list of connected clients (name, ip, connected_at)
//	GET  /plugin/probe?host=IP&port=N — TCP dial to verify a host:port is reachable via VPN
func (s *Server) handlePlugin(w http.ResponseWriter, r *http.Request) {
	if !s.checkAPIKey(w, r) {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/plugin")

	switch {
	case path == "/status" || path == "/status/":
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":              true,
			"active_clients":  s.metrics.activeConns.Load(),
			"uptime_seconds":  time.Since(s.metrics.startTime).Seconds(),
			"bytes_in_total":  s.metrics.bytesIn.Load(),
			"bytes_out_total": s.metrics.bytesOut.Load(),
		})

	case path == "/clients" || path == "/clients/":
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"clients": s.clients.Snapshot(),
		})

	case path == "/probe" || path == "/probe/":
		host := r.URL.Query().Get("host")
		port := r.URL.Query().Get("port")
		if host == "" || port == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host and port required"})
			return
		}
		if !isAllowedProbeHost(host) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host not allowed: only VPN subnet (10.8.0.0/24) and localhost are permitted"})
			return
		}
		addr := net.JoinHostPort(host, port)
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"reachable": false,
				"addr":      addr,
				"error":     fmt.Sprintf("dial: %v", err),
			})
			return
		}
		conn.Close()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"reachable": true,
			"addr":      addr,
		})

	default:
		http.NotFound(w, r)
	}
}
