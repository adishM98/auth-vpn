package httpguard

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckBrowser(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		host    string
		headers map[string]string
		body    string
		ok      bool
	}{
		{"dashboard GET on loopback", "GET", "127.0.0.1:9200", nil, "", true},
		{"dashboard JSON POST, same origin", "POST", "localhost:9200", map[string]string{"Origin": "http://localhost:9200", "Content-Type": "application/json"}, "{}", true},
		{"DELETE with no body", "DELETE", "127.0.0.1:9200", nil, "", true},
		{"DNS-rebound host name", "GET", "rebind.evil.example:9200", nil, "", false},
		{"cross-site origin", "POST", "127.0.0.1:9200", map[string]string{"Origin": "https://evil.example", "Content-Type": "application/json"}, "{}", false},
		{"text/plain simple POST", "POST", "127.0.0.1:9200", map[string]string{"Content-Type": "text/plain"}, `{"ip":"0.0.0.0/0"}`, false},
		{"Sec-Fetch-Site cross-site", "GET", "127.0.0.1:9200", map[string]string{"Sec-Fetch-Site": "cross-site"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "http://"+c.host+"/api/x", strings.NewReader(c.body))
			r.Host = c.host
			for k, v := range c.headers {
				r.Header.Set(k, v)
			}
			if err := CheckBrowser(r); (err == nil) != c.ok {
				t.Fatalf("CheckBrowser = %v, want ok=%v", err, c.ok)
			}
		})
	}
}
