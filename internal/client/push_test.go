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

func TestCheckTunnelAddrs(t *testing.T) {
	cases := []struct {
		name                     string
		subnet, clientIP, tunSrv string
		ok                       bool
	}{
		{"normal server", "10.8.0.0/24", "10.8.0.2", "10.8.0.1", true},
		{"half the internet as the subnet", "128.0.0.0/1", "128.0.0.2", "128.0.0.1", false},
		{"subnet covering the VPN server", "20.30.0.0/16", "20.30.0.2", "20.30.0.1", false},
		{"client address outside the subnet", "10.8.0.0/24", "8.8.8.8", "10.8.0.1", false},
		{"server address outside the subnet", "10.8.0.0/24", "10.8.0.2", "1.1.1.1", false},
		{"garbage", "nope", "10.8.0.2", "10.8.0.1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkTunnelAddrs(c.subnet, c.clientIP, c.tunSrv, "20.30.40.50")
			if (err == nil) != c.ok {
				t.Fatalf("checkTunnelAddrs = %v, want ok=%v", err, c.ok)
			}
		})
	}
}
