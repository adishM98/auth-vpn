package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

var localhostNet = func() *net.IPNet { _, n, _ := net.ParseCIDR("127.0.0.0/8"); return n }()

// isLocalTarget allows only localhost and the configured VPN subnet, preventing
// SSRF to cloud metadata endpoints or arbitrary hosts. Used by /plugin/probe
// and SSH direct-tcpip.
func (s *Server) isLocalTarget(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if localhostNet.Contains(ip) {
		return true
	}
	_, vpn, err := net.ParseCIDR(s.cfg.Subnet)
	return err == nil && vpn.Contains(ip)
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
		if !s.isLocalTarget(host) {
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
