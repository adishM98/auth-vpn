// Package httpguard stops a web page in the operator's browser from driving a
// local admin API that has no key configured.
package httpguard

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// CheckBrowser rejects requests a hostile web page could make:
//   - a Host header that is a DNS name other than localhost (DNS rebinding:
//     the attacker's domain re-resolves to 127.0.0.1),
//   - a cross-site Origin / Sec-Fetch-Site,
//   - a state-changing request that isn't application/json (CORS "simple"
//     text/plain or form posts skip the preflight).
//
// Only meant for keyless (open) mode: with a key, nothing ambient authorizes
// the request, and Host can legitimately be any DNS name.
func CheckBrowser(r *http.Request) error {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if net.ParseIP(strings.Trim(host, "[]")) == nil && !strings.EqualFold(host, "localhost") {
		return fmt.Errorf("host %q not allowed without an API key", r.Host)
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return fmt.Errorf("cross-origin request from %q", o)
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return fmt.Errorf("cross-site request")
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
	default:
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") && r.ContentLength != 0 {
			return fmt.Errorf("content type must be application/json")
		}
	}
	return nil
}
