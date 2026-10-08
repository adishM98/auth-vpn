package client

import (
	"reflect"
	"testing"
)

func TestSafeRoutes(t *testing.T) {
	got := safeRoutes([]string{
		"10.0.0.0/16",    // ok
		"0.0.0.0/0",      // default route: would hijack all traffic
		"20.0.0.0/7",     // too broad
		"20.30.0.0/16",   // contains the VPN server itself: would loop the tunnel
		"fd00::/8",       // IPv6: tunnel is IPv4-only
		"not-a-cidr",     // garbage
		"172.20.0.10/32", // ok
	}, "20.30.40.50")
	want := []string{"10.0.0.0/16", "172.20.0.10/32"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("safeRoutes = %v, want %v", got, want)
	}
}

func TestValidDNSDomain(t *testing.T) {
	for d, want := range map[string]bool{
		"cluster.local":        true,
		"corp.internal":        true,
		"svc.my-cluster.local": true,
		"local":                false, // single label: would capture a whole TLD
		"com":                  false,
		"../etc/passwd":        false, // becomes a path under /etc/resolver
		"a/b.local":            false,
		"":                     false,
		"Cluster.Local":        false,
		".cluster.local":       false,
	} {
		if got := validDNSDomain(d); got != want {
			t.Errorf("validDNSDomain(%q) = %v, want %v", d, got, want)
		}
	}
}
