package client

import (
	"log"
	"net"
	"regexp"
)

// safeRoutes filters server-pushed CIDRs down to ones that can't hijack the
// client: IPv4 only, no shorter than /8, and never covering the VPN server
// itself (that would route the tunnel through itself).
func safeRoutes(routes []string, serverIP string) []string {
	srv := net.ParseIP(serverIP)
	var ok []string
	for _, r := range routes {
		_, n, err := net.ParseCIDR(r)
		if err != nil || n.IP.To4() == nil {
			log.Printf("ignoring pushed route %q: not an IPv4 CIDR", r)
			continue
		}
		if ones, _ := n.Mask.Size(); ones < 8 {
			log.Printf("ignoring pushed route %s: broader than /8", r)
			continue
		}
		if srv != nil && n.Contains(srv) {
			log.Printf("ignoring pushed route %s: contains the VPN server address", r)
			continue
		}
		ok = append(ok, r)
	}
	return ok
}

// Lowercase, at least two labels. Also keeps the domain safe to use as a
// file name under /etc/resolver on macOS.
var dnsDomainRe = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`)

// validDNSDomain rejects pushed domains that are malformed, path-like, or a
// bare TLD (which would redirect lookups for a whole TLD to the server).
func validDNSDomain(d string) bool { return dnsDomainRe.MatchString(d) }

// serverIP resolves the host part of "host:port" to an IP for route checks.
func serverIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ips, _ := net.LookupIP(host)
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String()
		}
	}
	return host
}
