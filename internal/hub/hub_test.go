package hub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// upstream records what a registered server receives through /proxy.
type upstream struct {
	*httptest.Server
	got *http.Request
}

func newUpstream(t *testing.T, respond func(w http.ResponseWriter)) *upstream {
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.got = r.Clone(r.Context())
		respond(w)
	}))
	t.Cleanup(u.Close)
	return u
}

func proxy(h *Hub, method, target, host string, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Host = host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.withAuth(h.handleProxy)(w, r)
	return w
}

func jsonOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"ok":true}`) //nolint:errcheck
}

func TestProxy(t *testing.T) {
	t.Run("a cross-site text/plain POST can't use a keyless hub", func(t *testing.T) {
		up := newUpstream(t, jsonOK)
		h := New(&Config{Servers: []ServerEntry{{Name: "prod", URL: up.URL, APIKey: "srvkey"}}}, "")
		w := proxy(h, "POST", "/proxy/prod/api/whitelist", "127.0.0.1:9200", `{"name":"x","ip":"0.0.0.0/0"}`,
			map[string]string{"Origin": "https://evil.example", "Content-Type": "text/plain"})
		if w.Code != http.StatusForbidden || up.got != nil {
			t.Fatalf("status %d, upstream reached: %v", w.Code, up.got != nil)
		}
	})

	t.Run("a DNS-rebound host name can't use a keyless hub", func(t *testing.T) {
		up := newUpstream(t, jsonOK)
		h := New(&Config{Servers: []ServerEntry{{Name: "prod", URL: up.URL}}}, "")
		if w := proxy(h, "GET", "/api/hub/servers", "rebind.evil.example:9200", "", nil); w.Code != http.StatusForbidden {
			t.Fatalf("status %d, want 403", w.Code)
		}
	})

	t.Run("the dashboard's own JSON calls still work and get the server key", func(t *testing.T) {
		up := newUpstream(t, jsonOK)
		h := New(&Config{Servers: []ServerEntry{{Name: "prod", URL: up.URL, APIKey: "srvkey"}}}, "")
		w := proxy(h, "POST", "/proxy/prod/api/tokens", "127.0.0.1:9200", `{"name":"ci"}`,
			map[string]string{"Origin": "http://127.0.0.1:9200", "Content-Type": "application/json"})
		if w.Code != http.StatusOK || up.got.Header.Get("Authorization") != "Bearer srvkey" {
			t.Fatalf("status %d, upstream auth %q", w.Code, up.got.Header.Get("Authorization"))
		}
	})

	t.Run("the hub key never reaches an upstream", func(t *testing.T) {
		up := newUpstream(t, jsonOK)
		h := New(&Config{Servers: []ServerEntry{{Name: "a", URL: up.URL, APIKey: "srvkey"}}}, "hubsecret")
		proxy(h, "GET", "/proxy/a/api/status?key=hubsecret&x=1", "hub.example:9200", "", map[string]string{
			"Authorization": "Bearer hubsecret",
			"Referer":       "http://hub.example:9200/hub?key=hubsecret",
			"Cookie":        "session=hub",
		})
		if up.got == nil {
			t.Fatal("upstream not reached")
		}
		for _, leak := range []string{up.got.Header.Get("Referer"), up.got.Header.Get("Cookie"), up.got.URL.RawQuery} {
			if strings.Contains(leak, "hubsecret") || strings.Contains(leak, "session=hub") {
				t.Fatalf("hub credential forwarded upstream: %q", leak)
			}
		}
		if up.got.URL.Query().Get("x") != "1" {
			t.Fatalf("other query params dropped: %q", up.got.URL.RawQuery)
		}
	})

	t.Run("upstream HTML can't run as the hub", func(t *testing.T) {
		up := newUpstream(t, func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Set-Cookie", "x=1")
			io.WriteString(w, "<script>alert(1)</script>") //nolint:errcheck
		})
		h := New(&Config{Servers: []ServerEntry{{Name: "evil", URL: up.URL}}}, "")
		w := proxy(h, "GET", "/proxy/evil/x", "127.0.0.1:9200", "", nil)
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("proxied HTML not sandboxed: %v", w.Header())
		}
	})

	t.Run("upstream redirects are not followed", func(t *testing.T) {
		up := newUpstream(t, func(w http.ResponseWriter) {
			w.Header().Set("Location", "http://elsewhere.example/")
			w.WriteHeader(http.StatusFound)
		})
		h := New(&Config{Servers: []ServerEntry{{Name: "a", URL: up.URL, APIKey: "srvkey"}}}, "")
		if w := proxy(h, "GET", "/proxy/a/x", "127.0.0.1:9200", "", nil); w.Code != http.StatusFound {
			t.Fatalf("status %d, want the 302 passed back unfollowed", w.Code)
		}
	})
}
