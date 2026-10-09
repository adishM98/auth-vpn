package server

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Expose modes. In "labeled" mode only Services labelled exposeLabel=true are
// reachable through the tunnel; everything else is dropped by the server.
const (
	ExposeAll     = "all"
	ExposeLabeled = "labeled"
	exposeLabel   = "auth-vpn.io/expose"

	saDir        = "/var/run/secrets/kubernetes.io/serviceaccount"
	exposePoll   = 30 * time.Second
	maxListBytes = 8 << 20
)

type svcPort struct {
	IP    string
	Proto string // "tcp" | "udp"
	Port  uint16
}

// exposeSet is the current allow-list of ClusterIP:port pairs, refreshed from
// the Kubernetes API. The cluster DNS server on port 53 is always allowed so
// split DNS keeps working.
type exposeSet struct {
	mu    sync.RWMutex
	allow map[svcPort]bool
	dns   string
}

func newExposeSet(dnsIP string) *exposeSet {
	return &exposeSet{allow: map[svcPort]bool{}, dns: dnsIP}
}

func (e *exposeSet) set(ports []svcPort) {
	m := make(map[svcPort]bool, len(ports))
	for _, p := range ports {
		m[p] = true
	}
	e.mu.Lock()
	e.allow = m
	e.mu.Unlock()
}

func (e *exposeSet) allowed(ip, proto string, port uint16) bool {
	if ip == e.dns && port == 53 {
		return true
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.allow[svcPort{IP: ip, Proto: proto, Port: port}]
}

// routes returns one /32 per exposed ClusterIP, sorted, for AUTH_OK.
func (e *exposeSet) routes() []string {
	e.mu.RLock()
	seen := map[string]bool{}
	for p := range e.allow {
		seen[p.IP] = true
	}
	e.mu.RUnlock()
	out := make([]string, 0, len(seen))
	for ip := range seen {
		out = append(out, ip+"/32")
	}
	sort.Strings(out)
	return out
}

// allowPacket checks a client→network IPv4 packet against the allow-list.
// Only TCP/UDP to an exposed ip:port passes; ICMP, non-first fragments and
// anything unparsable are dropped (fail closed).
func (e *exposeSet) allowPacket(pkt []byte) bool {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return false
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl+4 {
		return false
	}
	if binary.BigEndian.Uint16(pkt[6:8])&0x1fff != 0 {
		return false // ponytail: no reassembly, so later fragments are dropped; fine for TCP (PMTU), rare for UDP
	}
	var proto string
	switch pkt[9] {
	case 6:
		proto = "tcp"
	case 17:
		proto = "udp"
	default:
		return false
	}
	dst := net.IP(pkt[16:20]).String()
	return e.allowed(dst, proto, binary.BigEndian.Uint16(pkt[ihl+2:ihl+4]))
}

// resolveTarget maps a proxy-mode dial target to an exposed IP. Names are
// resolved here, once, and the checked IP is what gets dialed, so a DNS
// answer can't change between the check and the dial.
func (e *exposeSet) resolveTarget(host string, port uint16, lookup func(string) ([]net.IP, error)) (string, error) {
	ips := []net.IP{net.ParseIP(host)}
	if ips[0] == nil {
		var err error
		if ips, err = lookup(host); err != nil {
			return "", err
		}
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil && e.allowed(ip4.String(), "tcp", port) {
			return ip4.String(), nil
		}
	}
	return "", fmt.Errorf("%s:%d is not exposed through auth-vpn (label the Service %s=true)", host, port, exposeLabel)
}

// parseServices extracts ClusterIP:port pairs from a Kubernetes ServiceList.
// Headless services (no ClusterIP) and IPv6 addresses are skipped.
func parseServices(body []byte) ([]svcPort, error) {
	var list struct {
		Items []struct {
			Spec struct {
				ClusterIP  string   `json:"clusterIP"`
				ClusterIPs []string `json:"clusterIPs"`
				Ports      []struct {
					Port     uint16 `json:"port"`
					Protocol string `json:"protocol"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	var out []svcPort
	for _, it := range list.Items {
		ips := it.Spec.ClusterIPs
		if len(ips) == 0 {
			ips = []string{it.Spec.ClusterIP}
		}
		for _, ip := range ips {
			if net.ParseIP(ip).To4() == nil {
				continue
			}
			for _, p := range it.Spec.Ports {
				proto := strings.ToLower(p.Protocol)
				if proto == "" {
					proto = "tcp"
				}
				out = append(out, svcPort{IP: ip, Proto: proto, Port: p.Port})
			}
		}
	}
	return out, nil
}

// fetchExposed lists Services labelled for exposure across all namespaces.
func fetchExposed(c *http.Client, apiBase, token string) ([]svcPort, error) {
	u := apiBase + "/api/v1/services?labelSelector=" + url.QueryEscape(exposeLabel+"=true")
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list services: %s (does the auth-vpn ServiceAccount have k8s/rbac.yaml applied?)", resp.Status)
	}
	return parseServices(body)
}

// watchExposed refreshes the allow-list from the in-cluster API every 30s.
// On error the last good list is kept; before the first success only cluster
// DNS is allowed (fail closed).
func (s *Server) watchExposed() {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" {
		log.Printf("expose=labeled: not running in Kubernetes — nothing will be reachable through the tunnel")
		return
	}
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(saDir + "/ca.crt"); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	base := "https://" + net.JoinHostPort(host, port)

	refresh := func() {
		token, err := os.ReadFile(saDir + "/token") // re-read: projected tokens rotate
		if err != nil {
			log.Printf("expose: read service account token: %v", err)
			return
		}
		ports, err := fetchExposed(client, base, strings.TrimSpace(string(token)))
		if err != nil {
			log.Printf("expose: %v (keeping previous list)", err)
			return
		}
		before := s.expose.routes()
		s.expose.set(ports)
		if after := s.expose.routes(); strings.Join(before, ",") != strings.Join(after, ",") {
			log.Printf("expose: %d service IPs reachable: %v", len(after), after)
		}
	}
	refresh()
	t := time.NewTicker(exposePoll)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			refresh()
		case <-s.done:
			return
		}
	}
}
