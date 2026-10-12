package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adishM98/auth-vpn/internal/tlsutil"
)

func TestAPITransport(t *testing.T) {
	t.Run("plain HTTP to a remote host is upgraded to https on the same port", func(t *testing.T) {
		for in, want := range map[string]string{
			"http://203.0.113.10:9100":  "https://203.0.113.10:9100",
			"https://203.0.113.10:9100": "https://203.0.113.10:9100",
			"http://localhost:9100":     "http://localhost:9100",
			"http://127.0.0.1:9100":     "http://127.0.0.1:9100",
		} {
			if got, err := secureAPIURL(in); err != nil || got != want {
				t.Fatalf("secureAPIURL(%q) = %q, %v; want %q", in, got, err, want)
			}
		}
	})

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://elsewhere.example/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	good := tlsutil.CertFingerprint(srv.Certificate())

	t.Run("the right fingerprint connects", func(t *testing.T) {
		resp, err := apiHTTPClient(false, good).Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	})

	t.Run("a different certificate is refused", func(t *testing.T) {
		if _, err := apiHTTPClient(false, "SHA256:not-this-one").Get(srv.URL); err == nil {
			t.Fatal("connected to a server with the wrong fingerprint")
		}
	})

	t.Run("redirects are not followed with the key", func(t *testing.T) {
		resp, err := apiHTTPClient(false, good).Get(srv.URL + "/redirect")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusFound {
			t.Fatalf("status %d: redirect was followed", resp.StatusCode)
		}
	})

}
