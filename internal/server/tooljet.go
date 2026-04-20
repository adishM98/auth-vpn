package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// handleToolJet serves the /tooljet/* routes used by the ToolJet datasource plugin.
// Routes:
//
//	GET  /tooljet/status          — server health + active client count
//	GET  /tooljet/clients         — list of connected clients (name, ip, connected_at)
//	GET  /tooljet/probe?host=IP&port=N — TCP dial to verify a host:port is reachable via VPN
func (s *Server) handleToolJet(w http.ResponseWriter, r *http.Request) {
	if !s.checkAPIKey(w, r) {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/tooljet")

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
