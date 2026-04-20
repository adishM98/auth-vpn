package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// startHTTPAPI starts the HTTP listener for metrics, health, API, Web UI, and ToolJet routes.
func (s *Server) startHTTPAPI() {
	mux := http.NewServeMux()

	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/api/clients", s.handleAPIClients)
	mux.HandleFunc("/api/tokens", s.handleAPITokens)
	mux.HandleFunc("/api/tokens/", s.handleAPITokenDelete)
	mux.HandleFunc("/tooljet/", s.handleToolJet)
	mux.HandleFunc("/ui", s.handleWebUI)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

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
	if !s.checkAPIKey(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"clients": s.clients.Snapshot(),
	})
}

func (s *Server) handleAPITokens(w http.ResponseWriter, r *http.Request) {
	if !s.checkAPIKey(w, r) {
		return
	}
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
	if !s.checkAPIKey(w, r) {
		return
	}
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

// checkAPIKey returns true if the request is authorized. When APIKey is empty,
// all requests are allowed (metrics-only mode).
func (s *Server) checkAPIKey(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.APIKey == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.TrimPrefix(auth, "Bearer ") != s.cfg.APIKey {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
