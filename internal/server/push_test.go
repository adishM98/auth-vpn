package server

import (
	"reflect"
	"testing"

	"github.com/adishM98/auth-vpn/pkg/protocol"
)

const podResolvConf = `search myns.svc.cluster.local svc.cluster.local cluster.local
nameserver 10.0.0.10
options ndots:5
`

func TestK8sPush(t *testing.T) {
	tests := []struct {
		name       string
		resolvConf string
		svcHost    string
		wantRoutes []string
		wantDNS    *protocol.DNSConfig
	}{
		{
			name:       "AKS-style private service CIDR guesses a /16 and reads cluster DNS",
			resolvConf: podResolvConf,
			svcHost:    "10.0.0.1",
			wantRoutes: []string{"10.0.0.0/16"},
			wantDNS:    &protocol.DNSConfig{Server: "10.0.0.10", Domains: []string{"cluster.local"}},
		},
		{
			name:       "GKE-style non-private service range guesses a narrower /20",
			resolvConf: "search a.svc.cluster.local svc.cluster.local\nnameserver 34.118.224.10\n",
			svcHost:    "34.118.224.1",
			wantRoutes: []string{"34.118.224.0/20"},
			wantDNS:    &protocol.DNSConfig{Server: "34.118.224.10", Domains: []string{"cluster.local"}},
		},
		{
			name:       "custom cluster domain is taken from the svc.* search entry",
			resolvConf: "search ns.svc.corp.internal svc.corp.internal corp.internal\nnameserver 172.20.0.10\n",
			svcHost:    "172.20.0.1",
			wantRoutes: []string{"172.20.0.0/16"},
			wantDNS:    &protocol.DNSConfig{Server: "172.20.0.10", Domains: []string{"corp.internal"}},
		},
		{
			name:       "not in a pod: nothing is pushed",
			resolvConf: "nameserver 8.8.8.8\n",
			svcHost:    "",
			wantRoutes: nil,
			wantDNS:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes, dns := k8sPush(tt.resolvConf, tt.svcHost)
			if !reflect.DeepEqual(routes, tt.wantRoutes) {
				t.Errorf("routes = %v, want %v", routes, tt.wantRoutes)
			}
			if !reflect.DeepEqual(dns, tt.wantDNS) {
				t.Errorf("dns = %+v, want %+v", dns, tt.wantDNS)
			}
		})
	}
}
