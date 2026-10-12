package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/adishM98/auth-vpn/internal/tlsutil"
)

func TestAPITransport(t *testing.T) {
	t.Run("plain HTTP to a remote host is refused, even with --insecure", func(t *testing.T) {
		if err := checkAPIURLSafety("http://203.0.113.10:9100"); err == nil {
			t.Fatal("plaintext API URL to a remote host accepted")
		}
		for _, ok := range []string{"https://203.0.113.10:9100", "http://localhost:9100", "http://127.0.0.1:9100"} {
			if err := checkAPIURLSafety(ok); err != nil {
				t.Fatalf("%s refused: %v", ok, err)
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
