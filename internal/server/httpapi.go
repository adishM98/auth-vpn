package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// startHTTPAPI starts the HTTP listener for metrics, health, API, Web UI, and ToolJet routes.
func (s *Server) startHTTPAPI() {
	mux := http.NewServeMux()

	mux.HandleFunc("/metrics", s.withAuth(s.handleMetrics))
	mux.HandleFunc("/health", s.withAuth(s.handleHealth))
	mux.HandleFunc("/api/clients", s.withAuth(s.handleAPIClients))
	mux.HandleFunc("/api/tokens", s.withAuth(s.handleAPITokens))
	mux.HandleFunc("/api/tokens/", s.withAuth(s.handleAPITokenDelete))
	mux.HandleFunc("/api/whitelist", s.withAuth(s.handleAPIWhitelist))
	mux.HandleFunc("/api/whitelist/", s.withAuth(s.handleAPIWhitelistDelete))
	mux.HandleFunc("/api/forwards", s.withAuth(s.handleAPIForwards))
	mux.HandleFunc("/api/forwards/", s.withAuth(s.handleAPIForwardsDelete))
	mux.HandleFunc("/tooljet/", s.handleToolJet)
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
		"status":  "ok",
		"uptime":  time.Since(s.metrics.startTime).String(),
		"clients": s.metrics.activeConns.Load(),
	})
}

func (s *Server) handleAPIClients(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"clients": s.clients.Snapshot(),
	})
}

func (s *Server) handleAPITokens(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"tokens": s.tokens.List(),
		})
	case http.MethodPost:
		var body struct {
			Name    string `json:"name"`
			OneTime bool   `json:"one_time"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
			return
		}
		raw, err := s.tokens.Add(body.Name, nil, body.OneTime)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"token": raw, "name": body.Name})
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

// withAuth wraps a handler to require API key authentication when APIKey is set.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAPIKey(w, r) {
			return
		}
		next(w, r)
	}
}

// checkAPIKey returns true if the request is authorized. When APIKey is empty,
// all requests are allowed (open mode — suitable for internal-only deployments).
func (s *Server) checkAPIKey(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.APIKey == "" {
		return true
	}
	// Accept Bearer token in Authorization header or ?key= query param (for browser direct access).
	auth := r.Header.Get("Authorization")
	if strings.TrimPrefix(auth, "Bearer ") == s.cfg.APIKey {
		return true
	}
	if r.URL.Query().Get("key") == s.cfg.APIKey {
		return true
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	return false
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
