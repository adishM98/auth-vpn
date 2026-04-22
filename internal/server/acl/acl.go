package acl

import (
	"encoding/binary"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// Policy is the top-level structure of acl.yaml.
type Policy struct {
	DefaultPolicy string `yaml:"default_policy"` // "allow" or "deny"
	Rules         []Rule `yaml:"rules"`
}

// Rule defines allow/deny port specs for a named device (token name).
type Rule struct {
	Device string     `yaml:"device"`
	Allow  []PortSpec `yaml:"allow,omitempty"`
	Deny   []PortSpec `yaml:"deny,omitempty"`
}

// PortSpec matches a protocol and optional port (0 = any port).
type PortSpec struct {
	Proto string `yaml:"proto"` // "tcp", "udp", "icmp", or "*"
	Port  int    `yaml:"port"`  // 0 = match any port
}

// Engine enforces ACL rules at the packet level.
type Engine struct {
	mu     sync.RWMutex
	policy Policy
	index  map[string]*Rule // device name → rule (nil = use default policy)
	path   string
}

// Load parses an acl.yaml file and returns a ready Engine.
func Load(path string) (*Engine, error) {
	e := &Engine{path: path}
	return e, e.Reload()
}

// Reload re-reads the ACL file from disk. Safe to call from a SIGHUP handler.
func (e *Engine) Reload() error {
	data, err := os.ReadFile(e.path)
	if err != nil {
		if os.IsNotExist(err) {
			// No ACL file = allow everything.
			e.mu.Lock()
			e.policy = Policy{DefaultPolicy: "allow"}
			e.index = nil
			e.mu.Unlock()
			return nil
		}
		return err
	}

	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.DefaultPolicy == "" {
		p.DefaultPolicy = "allow"
	}

	idx := make(map[string]*Rule, len(p.Rules))
	for i := range p.Rules {
		r := &p.Rules[i]
		idx[r.Device] = r
	}

	e.mu.Lock()
	e.policy = p
	e.index = idx
	e.mu.Unlock()
	return nil
}

// Allow returns true if the given device is permitted to send pkt to the
// server. pkt must be a valid IPv4 packet (at least 20 bytes).
func (e *Engine) Allow(deviceName string, pkt []byte) bool {
	if len(pkt) < 20 {
		return false // too short to be a valid IPv4 packet; deny
	}

	e.mu.RLock()
	rule, hasRule := e.index[deviceName]
	defaultAllow := e.policy.DefaultPolicy != "deny"
	e.mu.RUnlock()

	if !hasRule {
		return defaultAllow
	}

	proto, dstPort := extractProtoPort(pkt)

	// Explicit deny list checked first.
	for _, spec := range rule.Deny {
		if matches(spec, proto, dstPort) {
			return false
		}
	}

	// If there's an allow list, the packet must match it.
	if len(rule.Allow) > 0 {
		for _, spec := range rule.Allow {
			if matches(spec, proto, dstPort) {
				return true
			}
		}
		return false // allow list present but no match
	}

	return defaultAllow
}

// extractProtoPort reads the IPv4 protocol field (byte 9) and, for TCP/UDP,
// the destination port from the transport header (bytes 22-23).
func extractProtoPort(pkt []byte) (proto string, dstPort int) {
	switch pkt[9] {
	case 6:
		proto = "tcp"
	case 17:
		proto = "udp"
	case 1:
		proto = "icmp"
	default:
		proto = "*"
	}

	ihl := int(pkt[0]&0x0f) * 4 // IP header length (min 20)
	if ihl < 20 {
		return // malformed IHL; leave dstPort = 0
	}
	if (proto == "tcp" || proto == "udp") && len(pkt) >= ihl+4 {
		dstPort = int(binary.BigEndian.Uint16(pkt[ihl+2 : ihl+4]))
	}
	return
}

func matches(spec PortSpec, proto string, dstPort int) bool {
	if spec.Proto != "*" && spec.Proto != proto {
		return false
	}
	if spec.Port != 0 && spec.Port != dstPort {
		return false
	}
	return true
}
