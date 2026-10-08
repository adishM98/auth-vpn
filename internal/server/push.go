package server

import (
	"log"
	"net"
	"os"
	"strings"

	"github.com/adishM98/auth-vpn/pkg/protocol"
)

// resolvePush decides which routes and DNS settings are pushed to TUN clients
// in AUTH_OK. Precedence per field: server.yaml > AUTH_VPN_PUSH_ROUTES env
// (routes only) > Kubernetes auto-detection. NoPush turns all of it off.
func (cfg *Config) resolvePush() {
	if cfg.NoPush {
		cfg.PushRoutes, cfg.PushDNS = nil, nil
		return
	}
	if len(cfg.PushRoutes) == 0 {
		if env := os.Getenv("AUTH_VPN_PUSH_ROUTES"); env != "" {
			for _, r := range strings.Split(env, ",") {
				if r = strings.TrimSpace(r); r != "" {
					cfg.PushRoutes = append(cfg.PushRoutes, r)
				}
			}
		}
	}
	resolvConf, _ := os.ReadFile("/etc/resolv.conf")
	routes, dns := k8sPush(string(resolvConf), os.Getenv("KUBERNETES_SERVICE_HOST"))
	if len(cfg.PushRoutes) == 0 && routes != nil {
		cfg.PushRoutes = routes
		log.Printf("kubernetes: guessed service CIDR %v — set AUTH_VPN_PUSH_ROUTES or push_routes in server.yaml if wrong", routes)
	}
	if cfg.PushDNS == nil && dns != nil {
		cfg.PushDNS = dns
		log.Printf("kubernetes: pushing cluster DNS %s for %v", dns.Server, dns.Domains)
	}
	if len(cfg.PushRoutes) > 0 {
		log.Printf("pushing routes to clients: %v", cfg.PushRoutes)
	}
}

// k8sPush derives push settings from a pod's resolv.conf and the
// KUBERNETES_SERVICE_HOST env var. Returns nils when not running in a pod.
func k8sPush(resolvConf, svcHost string) ([]string, *protocol.DNSConfig) {
	ip := net.ParseIP(svcHost).To4()
	if ip == nil {
		return nil, nil
	}

	// The API server's ClusterIP sits at the start of the service CIDR, but its
	// mask isn't visible from inside a pod.
	// ponytail: fixed-size guess (/16 private, /20 for GKE-style public ranges);
	// query the ServiceCIDR API (k8s 1.33+) if guesses prove wrong in practice.
	bits := 16
	if !ip.IsPrivate() {
		bits = 20
	}
	routes := []string{(&net.IPNet{IP: ip.Mask(net.CIDRMask(bits, 32)), Mask: net.CIDRMask(bits, 32)}).String()}

	var nameserver, domain string
	for _, line := range strings.Split(resolvConf, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "nameserver":
			if nameserver == "" {
				nameserver = f[1]
			}
		case "search":
			// kubelet writes "<ns>.svc.<domain> svc.<domain> <domain>".
			for _, s := range f[1:] {
				if strings.HasPrefix(s, "svc.") {
					domain = strings.TrimPrefix(s, "svc.")
					break
				}
			}
		}
	}
	if net.ParseIP(nameserver).To4() == nil || domain == "" {
		return routes, nil
	}
	return routes, &protocol.DNSConfig{Server: nameserver, Domains: []string{domain}}
}
