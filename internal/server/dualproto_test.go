package server

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestDualListenerServesHTTPAndHTTPSOnOnePort(t *testing.T) {
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := generateSelfSignedCert("127.0.0.1", cert, key); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newDualListener(inner, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			io.WriteString(w, "https") //nolint:errcheck
		} else {
			io.WriteString(w, "http") //nolint:errcheck
		}
	})}
	go srv.Serve(l) //nolint:errcheck
	defer srv.Close()

	get := func(c *http.Client, url string) string {
		t.Helper()
		resp, err := c.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	addr := inner.Addr().String()
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

	if got := get(http.DefaultClient, "http://"+addr+"/"); got != "http" {
		t.Errorf("plain request served as %q, want http", got)
	}
	if got := get(insecure, "https://"+addr+"/"); got != "https" {
		t.Errorf("TLS request served as %q, want https (r.TLS must be set)", got)
	}
}

func TestPlainHTTPOnlyFromPrivatePaths(t *testing.T) {
	s := &Server{cfg: &Config{Subnet: "10.8.0.0/24"}}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.plainHTTPOnlyFromPrivate(ok)

	tests := []struct {
		name, remote string
		tls          bool
		wantCode     int
	}{
		{"https from anywhere is served", "203.0.113.5:4000", true, http.StatusOK},
		{"http from loopback (kubectl port-forward) is served", "127.0.0.1:4000", false, http.StatusOK},
		{"http from a VPN client (already inside the TLS tunnel) is served", "10.8.0.2:4000", false, http.StatusOK},
		{"http from the internet is redirected to https", "203.0.113.5:4000", false, http.StatusPermanentRedirect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://4.249.241.157:9100/ui?x=1", nil)
			r.RemoteAddr = tt.remote
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantCode)
			}
			if tt.wantCode == http.StatusPermanentRedirect {
				if loc := w.Header().Get("Location"); loc != "https://4.249.241.157:9100/ui?x=1" {
					t.Errorf("Location = %q", loc)
				}
			}
		})
	}
}
