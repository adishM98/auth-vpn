package hub

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/adishM98/auth-vpn/internal/tlsutil"
)

type serverStatus struct {
	Online        bool      `json:"online"`
	ActiveClients int64     `json:"active_clients"`
	UptimeSecs    float64   `json:"uptime_seconds"`
	LastChecked   time.Time `json:"last_checked"`
	Error         string    `json:"error,omitempty"`
}

// Hub is the multi-server management hub.
type Hub struct {
	cfg      Config
	cfgMu    sync.RWMutex
	statuses map[string]serverStatus
	statusMu sync.RWMutex
	hcache   map[string]*http.Client // per-server http.Client, keyed by server name
	hcMu     sync.RWMutex
	done     chan struct{}
	hubKey   string
	pollCtx  context.Context    // cancelled when Stop() is called; used for all poll requests
	pollStop context.CancelFunc // cancels pollCtx
}

// New creates a Hub from the given config. hubKey protects the hub's own API
// when set; pass "" to allow unauthenticated access (localhost-only recommended).
func New(cfg *Config, hubKey string) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		cfg:      snapshotConfig(cfg),
		statuses: make(map[string]serverStatus),
		hcache:   make(map[string]*http.Client),
		done:     make(chan struct{}),
		hubKey:   hubKey,
		pollCtx:  ctx,
		pollStop: cancel,
	}
	h.rebuildClientCache()
	return h
}

// Start binds the hub HTTP server on addr and blocks until stopped or error.
func (h *Hub) Start(addr string) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/hub", h.withAuth(h.handleWebUI))
	mux.HandleFunc("/hub/open/", h.withAuth(h.handleOpen))
	mux.HandleFunc("/api/hub/servers/probe", h.withAuth(h.handleProbe))
	mux.HandleFunc("/api/hub/servers/", h.withAuth(h.handleServerByName))
	mux.HandleFunc("/api/hub/servers", h.withAuth(h.handleServers))
	mux.HandleFunc("/api/hub/overview", h.withAuth(h.handleOverview))
	mux.HandleFunc("/proxy/", h.withAuth(h.handleProxy))
	mux.HandleFunc("/", h.withAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/hub", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		<-h.done
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx) //nolint:errcheck
	}()
	go h.pollLoop()

	log.Printf("hub dashboard: http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop shuts the hub down.
func (h *Hub) Stop() { h.pollStop(); close(h.done) }

// ── Health polling ────────────────────────────────────────────────────────────

func (h *Hub) pollLoop() {
	h.pollAll(h.pollCtx)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			h.pollAll(h.pollCtx)
		case <-h.done:
			return
		}
	}
}

func (h *Hub) pollAll(ctx context.Context) {
	h.cfgMu.RLock()
	servers := make([]ServerEntry, len(h.cfg.Servers))
	copy(servers, h.cfg.Servers)
	h.cfgMu.RUnlock()
	for _, s := range servers {
		go h.pollOne(ctx, s)
	}
}

func (h *Hub) pollOne(ctx context.Context, s ServerEntry) {
	client := h.clientFor(s)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.URL+"/health", nil)
	if err != nil {
		h.setStatus(s.Name, serverStatus{Online: false, LastChecked: time.Now(), Error: err.Error()})
		return
	}
	if s.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		h.setStatus(s.Name, serverStatus{Online: false, LastChecked: time.Now(), Error: err.Error()})
		return
	}
	defer resp.Body.Close()
	var body struct {
		Uptime  float64 `json:"uptime"`
		Clients int64   `json:"clients"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&body) //nolint:errcheck
	h.setStatus(s.Name, serverStatus{
		Online:        resp.StatusCode == http.StatusOK,
		ActiveClients: body.Clients,
		UptimeSecs:    body.Uptime,
		LastChecked:   time.Now(),
	})
}

func (h *Hub) setStatus(name string, st serverStatus) {
	h.statusMu.Lock()
	h.statuses[name] = st
	h.statusMu.Unlock()
}

func (h *Hub) getStatus(name string) serverStatus {
	h.statusMu.RLock()
	defer h.statusMu.RUnlock()
	return h.statuses[name]
}

// ── HTTP client cache ─────────────────────────────────────────────────────────

func (h *Hub) rebuildClientCache() {
	h.cfgMu.RLock()
	servers := make([]ServerEntry, len(h.cfg.Servers))
	copy(servers, h.cfg.Servers)
	h.cfgMu.RUnlock()

	h.hcMu.Lock()
	defer h.hcMu.Unlock()
	h.hcache = make(map[string]*http.Client, len(servers))
	for _, s := range servers {
		h.hcache[s.Name] = buildHTTPClient(s)
	}
}

func (h *Hub) clientFor(s ServerEntry) *http.Client {
	h.hcMu.RLock()
	c, ok := h.hcache[s.Name]
	h.hcMu.RUnlock()
	if ok {
		return c
	}
	return buildHTTPClient(s)
}

func buildHTTPClient(s ServerEntry) *http.Client {
	if s.TLSFingerprint != "" {
		return &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsutil.PinnedTLSConfig(s.TLSFingerprint)},
			Timeout:   15 * time.Second,
		}
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}},
		Timeout:   15 * time.Second,
	}
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func (h *Hub) handleWebUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(hubUIHTML) //nolint:errcheck
}

type serverInfo struct {
	Name          string    `json:"name"`
	URL           string    `json:"url"`
	Online        bool      `json:"online"`
	ActiveClients int64     `json:"active_clients"`
	UptimeSecs    float64   `json:"uptime_seconds"`
	LastChecked   time.Time `json:"last_checked"`
	Error         string    `json:"error,omitempty"`
}

// GET /api/hub/servers  — list all servers with live status
// POST /api/hub/servers — add a new server
func (h *Hub) handleServers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.cfgMu.RLock()
		servers := make([]ServerEntry, len(h.cfg.Servers))
		copy(servers, h.cfg.Servers)
		h.cfgMu.RUnlock()

		result := make([]serverInfo, 0, len(servers))
		for _, s := range servers {
			st := h.getStatus(s.Name)
			result = append(result, serverInfo{
				Name:          s.Name,
				URL:           s.URL,
				Online:        st.Online,
				ActiveClients: st.ActiveClients,
				UptimeSecs:    st.UptimeSecs,
				LastChecked:   st.LastChecked,
				Error:         st.Error,
			})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"servers": result})

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var body struct {
			Name           string `json:"name"`
			URL            string `json:"url"`
			APIKey         string `json:"api_key"`
			TLSFingerprint string `json:"tls_fingerprint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
			return
		}
		if body.Name == "" || body.URL == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and url required"})
			return
		}
		parsed, err := url.Parse(body.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url must be http:// or https://"})
			return
		}

		entry := ServerEntry{
			Name:           body.Name,
			URL:            strings.TrimRight(body.URL, "/"),
			APIKey:         body.APIKey,
			TLSFingerprint: body.TLSFingerprint,
		}

		h.cfgMu.Lock()
		for _, s := range h.cfg.Servers {
			if s.Name == body.Name {
				h.cfgMu.Unlock()
				writeJSON(w, http.StatusConflict, map[string]string{"error": "server name already exists"})
				return
			}
		}
		h.cfg.Servers = append(h.cfg.Servers, entry)
		snap := snapshotConfig(&h.cfg)
		h.cfgMu.Unlock()

		if err := saveConfig(&snap); err != nil {
			log.Printf("hub: failed to save config: %v", err)
		}
		h.hcMu.Lock()
		h.hcache[entry.Name] = buildHTTPClient(entry)
		h.hcMu.Unlock()
		go h.pollOne(h.pollCtx, entry)

		writeJSON(w, http.StatusCreated, map[string]string{"name": entry.Name})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// DELETE /api/hub/servers/{name} — remove a server
func (h *Hub) handleServerByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/hub/servers/")
	if name == "" || name == "probe" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}

	h.cfgMu.Lock()
	newList := make([]ServerEntry, 0, len(h.cfg.Servers))
	found := false
	for _, s := range h.cfg.Servers {
		if s.Name == name {
			found = true
		} else {
			newList = append(newList, s)
		}
	}
	if !found {
		h.cfgMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "server not found"})
		return
	}
	h.cfg.Servers = newList
	snap := snapshotConfig(&h.cfg)
	h.cfgMu.Unlock()

	if err := saveConfig(&snap); err != nil {
		log.Printf("hub: failed to save config: %v", err)
	}

	h.statusMu.Lock()
	delete(h.statuses, name)
	h.statusMu.Unlock()

	h.hcMu.Lock()
	delete(h.hcache, name)
	h.hcMu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{"removed": name})
}

// POST /api/hub/servers/probe — fetch TLS fingerprint without saving
func (h *Hub) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var body struct {
		URL    string `json:"url"`
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	parsed, err := url.Parse(body.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid url: " + err.Error()})
		return
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url must be http:// or https://"})
		return
	}

	// HTTP: no fingerprint, just verify reachability via /health
	if parsed.Scheme != "https" {
		client := &http.Client{Timeout: 10 * time.Second}
		req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(body.URL, "/")+"/health", nil)
		if body.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+body.APIKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"reachable": false, "error": err.Error()})
			return
		}
		resp.Body.Close()
		writeJSON(w, http.StatusOK, map[string]interface{}{"reachable": true, "fingerprint": ""})
		return
	}

	// HTTPS: fetch TLS fingerprint
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	fp, err := tlsutil.FetchFingerprint(fmt.Sprintf("%s:%s", host, port))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"reachable": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"reachable": true, "fingerprint": fp})
}

// GET /api/hub/overview — aggregate stats across all servers
func (h *Hub) handleOverview(w http.ResponseWriter, r *http.Request) {
	h.cfgMu.RLock()
	total := len(h.cfg.Servers)
	h.cfgMu.RUnlock()

	var online int
	var totalClients int64
	h.statusMu.RLock()
	for _, st := range h.statuses {
		if st.Online {
			online++
			totalClients += st.ActiveClients
		}
	}
	h.statusMu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total_servers":  total,
		"servers_online": online,
		"total_clients":  totalClients,
	})
}

// GET /hub/open/{serverName} — redirect to that server's native dashboard,
// injecting the API key so the browser lands already authenticated.
func (h *Hub) handleOpen(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/hub/open/")
	if name == "" {
		http.NotFound(w, r)
		return
	}

	h.cfgMu.RLock()
	var entry *ServerEntry
	for i := range h.cfg.Servers {
		if h.cfg.Servers[i].Name == name {
			cp := h.cfg.Servers[i]
			entry = &cp
			break
		}
	}
	h.cfgMu.RUnlock()

	if entry == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "server not found: " + name})
		return
	}

	base, err := url.Parse(entry.URL)
	if err != nil || base.Host == "" {
		http.Error(w, "invalid server url", http.StatusInternalServerError)
		return
	}
	target := base.Scheme + "://" + base.Host + "/ui"
	if entry.APIKey != "" {
		target += "?key=" + url.QueryEscape(entry.APIKey)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// /proxy/{serverName}/... — transparent reverse proxy to a registered server
func (h *Hub) handleProxy(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/proxy/")
	idx := strings.Index(trimmed, "/")

	var serverName, upstreamPath string
	if idx < 0 {
		serverName = trimmed
		upstreamPath = "/"
	} else {
		serverName = trimmed[:idx]
		upstreamPath = trimmed[idx:]
	}
	if serverName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server name required"})
		return
	}

	h.cfgMu.RLock()
	var entry *ServerEntry
	for i := range h.cfg.Servers {
		if h.cfg.Servers[i].Name == serverName {
			cp := h.cfg.Servers[i]
			entry = &cp
			break
		}
	}
	h.cfgMu.RUnlock()

	if entry == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "server not found: " + serverName})
		return
	}

	cleanPath := path.Clean(upstreamPath)
	if strings.Contains(cleanPath, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}

	targetURL := entry.URL + cleanPath
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for k, vv := range r.Header {
		if !isHopByHop(k) {
			req.Header[k] = vv
		}
	}
	req.Header.Del("Host")
	if entry.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+entry.APIKey)
	} else {
		req.Header.Del("Authorization")
	}

	resp, err := h.clientFor(*entry).Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		if !isHopByHop(k) {
			w.Header()[k] = vv
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// ── Auth middleware ───────────────────────────────────────────────────────────

func (h *Hub) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.hubKey == "" {
			next(w, r)
			return
		}
		key := []byte(h.hubKey)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), key) == 1 {
			next(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("key")), key) == 1 {
			next(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailers":            true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func isHopByHop(header string) bool {
	return hopByHopHeaders[http.CanonicalHeaderKey(header)]
}
