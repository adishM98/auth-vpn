package server

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/adishM98/auth-vpn/internal/auth"
	"github.com/adishM98/auth-vpn/internal/httpguard"
)

// startHTTPAPI starts the HTTP listener for metrics, health, API, Web UI, and plugin routes.
func (s *Server) startHTTPAPI() {
	mux := http.NewServeMux()

	mux.HandleFunc("/metrics", s.withAuth(s.handleMetrics))
	mux.HandleFunc("/health", s.withAuth(s.handleHealth))
	mux.HandleFunc("/api/clients", s.withAuth(s.handleAPIClients))
	mux.HandleFunc("/api/clients/", s.withAuth(s.handleAPIClientDisconnect))
	mux.HandleFunc("/api/tokens", s.withAuth(s.handleAPITokens))
	mux.HandleFunc("/api/tokens/", s.withAuth(s.handleAPITokenDelete))
	mux.HandleFunc("/api/whitelist", s.withAuth(s.handleAPIWhitelist))
	mux.HandleFunc("/api/whitelist/", s.withAuth(s.handleAPIWhitelistDelete))
	mux.HandleFunc("/api/forwards", s.withAuth(s.handleAPIForwards))
	mux.HandleFunc("/api/forwards/", s.withAuth(s.handleAPIForwardsDelete))
	mux.HandleFunc("/api/ssh-keys", s.withAuth(s.handleAPISSHKeys))
	mux.HandleFunc("/api/ssh-keys/generate", s.withAuth(s.handleAPISSHKeysGenerate))
	mux.HandleFunc("/api/ssh-keys/", s.withAuth(s.handleAPISSHKeysDelete))
	mux.HandleFunc("/plugin/", s.handlePlugin)
	mux.HandleFunc("/ui", s.withAuth(s.handleWebUI))
	mux.HandleFunc("/", s.withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))

	srv := &http.Server{
		Addr:         s.cfg.MetricsAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		<-s.done
		srv.Close()
	}()

	// With cert/key (same files as the tunnel), serve https and http on the
	// same port: https from anywhere, plain http only over private paths
	// (localhost / VPN subnet), and a redirect to https otherwise.
	if s.cfg.TLSCert != "" && s.cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey)
		if err != nil {
			log.Printf("HTTP API error: load TLS keypair: %v", err)
			return
		}
		ln, err := net.Listen("tcp", s.cfg.MetricsAddr)
		if err != nil {
			log.Printf("HTTP API error: %v", err)
			return
		}
		srv.Handler = s.plainHTTPOnlyFromPrivate(mux)
		log.Printf("HTTP API listening on https://%s and http:// (plain http only from localhost and %s) — metrics, /ui, /api", s.cfg.MetricsAddr, s.cfg.Subnet)
		dl := newDualListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		if err := srv.Serve(dl); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP API error: %v", err)
		}
		return
	}
	log.Printf("HTTP API listening on http://%s (metrics, /ui, /api)", s.cfg.MetricsAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("HTTP API error: %v", err)
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.metrics.WritePrometheus(w)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"uptime":      time.Since(s.metrics.startTime).Seconds(),
		"clients":     s.metrics.activeConns.Load(),
		"tunnel_ip":   s.cfg.ServerIP,
		"tunnel_port": s.cfg.Port,
		"subnet":      s.cfg.Subnet,
		"bytes_in":    s.metrics.bytesIn.Load(),
		"bytes_out":   s.metrics.bytesOut.Load(),
	})
}

func (s *Server) handleAPIClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"clients": append(s.clients.Snapshot(), s.sessions.nonTUN()...),
	})
}

func (s *Server) handleAPIClientDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/clients/")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	// Every session with this name: TUN, proxy and SSH.
	if s.sessions.closeWhere(name) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "client not found"})
		return
	}
	log.Printf("admin disconnected client %q", name)
	writeJSON(w, http.StatusOK, map[string]string{"disconnected": name})
}

func (s *Server) handleAPITokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"tokens": s.tokens.List(),
		})
	case http.MethodPost:
		var body struct {
			Name      string `json:"name"`
			OneTime   bool   `json:"one_time"`
			ExpiresIn string `json:"expires_in"` // optional, e.g. "2h" or "30m"
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
			return
		}
		var expiresAt *time.Time
		if body.ExpiresIn != "" {
			d, err := time.ParseDuration(body.ExpiresIn)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid expires_in: " + err.Error()})
				return
			}
			t := time.Now().UTC().Add(d)
			expiresAt = &t
		}
		name := body.Name
		raw, err := s.tokens.Add(name, expiresAt, body.OneTime)
		// Older CLIs name CI tokens gh-RUN-JOB-ATTEMPT, which repeats across
		// matrix legs. They revoke by the name we return, so give a colliding
		// ephemeral name a suffix instead of failing the job.
		for i := 0; errors.Is(err, auth.ErrNameTaken) && isEphemeralName(body.Name) && i < 5; i++ {
			name = body.Name + "-" + randSuffix()
			raw, err = s.tokens.Add(name, expiresAt, body.OneTime)
		}
		if errors.Is(err, auth.ErrNameTaken) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"token": raw, "name": name})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPITokenDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	// path: /api/tokens/{name}
	name := strings.TrimPrefix(r.URL.Path, "/api/tokens/")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	if err := s.tokens.Revoke(name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// Revoking also ends the sessions the token opened (tunnel and SSH).
	if n := s.sessions.closeWhere(name, viaToken); n > 0 {
		log.Printf("token %q revoked: closed %d live session(s)", name, n)
	}
	writeJSON(w, http.StatusOK, map[string]string{"revoked": name})
}

func (s *Server) handleAPIWhitelist(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"whitelist": s.whitelist.List(),
		})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
			IP   string `json:"ip"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.IP == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and ip required"})
			return
		}
		if err := s.whitelist.Add(body.Name, body.IP); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"name": body.Name, "ip": body.IP})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPIWhitelistDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/whitelist/")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	// Capture the IP before removal so we can kill active connections.
	ip := s.whitelist.IPForName(name)
	if err := s.whitelist.Remove(name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if ip != "" {
		go s.killDirectConns(ip)
	}
	s.sessions.closeWhere(name, viaWhitelist) // tunnel sessions admitted by this entry
	writeJSON(w, http.StatusOK, map[string]string{"removed": name})
}

func (s *Server) handleAPIForwards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.beMu.RLock()
		errs := make(map[int]string, len(s.bindErrors))
		for k, v := range s.bindErrors {
			errs[k] = v
		}
		s.beMu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"forwards":    s.forwards.List(),
			"bind_errors": errs,
		})
	case http.MethodPost:
		var body struct {
			ListenPort int    `json:"listen_port"`
			Target     string `json:"target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ListenPort == 0 || body.Target == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "listen_port and target required"})
			return
		}
		if err := s.forwards.Add(body.ListenPort, body.Target); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		// Start the listener immediately — no server restart needed.
		go s.startDirectListener(DirectForward{ListenPort: body.ListenPort, Target: body.Target})
		log.Printf("direct forward added: :%d → %s", body.ListenPort, body.Target)
		writeJSON(w, http.StatusCreated, map[string]interface{}{
			"listen_port": body.ListenPort, "target": body.Target,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPIForwardsDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	portStr := strings.TrimPrefix(r.URL.Path, "/api/forwards/")
	port, err := strconv.Atoi(portStr)
	if err != nil || port == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid port required"})
		return
	}
	if err := s.forwards.Remove(port); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.stopDirectListener(port)
	log.Printf("direct forward removed: :%d", port)
	writeJSON(w, http.StatusOK, map[string]interface{}{"removed_port": port})
}

func (s *Server) handleAPISSHKeys(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ssh_keys": s.sshKeys.List(),
		})
	case http.MethodPost:
		var body struct {
			Name      string `json:"name"`
			PublicKey string `json:"public_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" || body.PublicKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and public_key required"})
			return
		}
		if err := s.sshKeys.Add(body.Name, body.PublicKey); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"name": body.Name})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleAPISSHKeysGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	privKey, err := s.sshKeys.Generate(body.Name)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Private key returned once — never stored server-side.
	writeJSON(w, http.StatusCreated, map[string]string{
		"name":        body.Name,
		"private_key": privKey,
	})
}

func (s *Server) handleAPISSHKeysDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/ssh-keys/")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	if err := s.sshKeys.Remove(name); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	s.sessions.closeWhere(name, viaSSHKey)
	writeJSON(w, http.StatusOK, map[string]string{"removed": name})
}

// withAuth wraps a handler to require API key authentication when APIKey is set.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAPIKey(w, r) {
			return
		}
		next(w, r)
	}
}

// checkAPIKey returns true if the request is authorized. With no APIKey (open
// mode: `server ui-dev`, or an explicit empty --api-key) any caller is allowed,
// except requests a web page could forge from the operator's browser.
func (s *Server) checkAPIKey(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.APIKey == "" {
		if err := httpguard.CheckBrowser(r); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return false
		}
		return true
	}
	// Accept Bearer token in Authorization header or ?key= query param (for browser direct access).
	if keyEqual(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), s.cfg.APIKey) {
		return true
	}
	if keyEqual(r.URL.Query().Get("key"), s.cfg.APIKey) {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	return false
}

// isEphemeralName matches the names `connect --api-key/--github-action` uses.
func isEphemeralName(n string) bool {
	return strings.HasPrefix(n, "gh-") || strings.HasPrefix(n, "ephemeral-")
}

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func keyEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
