package acl

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// tcpPacket builds a minimal IPv4/TCP header with the given ports.
func tcpPacket(srcPort, dstPort uint16) []byte {
	p := make([]byte, 40)
	p[0], p[9] = 0x45, 6
	binary.BigEndian.PutUint16(p[20:], srcPort)
	binary.BigEndian.PutUint16(p[22:], dstPort)
	return p
}

func load(t *testing.T, yaml string) *Engine {
	t.Helper()
	path := filepath.Join(t.TempDir(), "acl.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	e, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestACL(t *testing.T) {
	onlyPostgres := load(t, "default_policy: deny\nrules:\n  - device: dev-alice\n    allow:\n      - proto: tcp\n        port: 5432\n")

	t.Run("an allowed service is reachable from any client port", func(t *testing.T) {
		if !onlyPostgres.Allow("dev-alice", tcpPacket(51234, 5432)) {
			t.Fatal("request to the allowed port was denied")
		}
	})

	t.Run("binding the client source port to an allowed port does not open other services", func(t *testing.T) {
		if onlyPostgres.Allow("dev-alice", tcpPacket(5432, 22)) {
			t.Fatal("request to :22 from source port 5432 was allowed")
		}
	})

	t.Run("deny rules match outbound connections", func(t *testing.T) {
		noSSH := load(t, "default_policy: allow\nrules:\n  - device: dev-alice\n    deny:\n      - proto: tcp\n        port: 22\n")
		if noSSH.Allow("dev-alice", tcpPacket(40000, 22)) {
			t.Fatal("outbound SSH not denied")
		}
	})

	t.Run("proxy and SSH dials use the same rules", func(t *testing.T) {
		if onlyPostgres.AllowPort("dev-alice", "tcp", 22) || !onlyPostgres.AllowPort("dev-alice", "tcp", 5432) {
			t.Fatal("AllowPort disagrees with the policy")
		}
	})

	t.Run("a malformed file is an error and DenyAll blocks everyone until it's fixed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "acl.yaml")
		_ = os.WriteFile(path, []byte("rules: [unclosed"), 0o600)
		e, err := Load(path)
		if err == nil {
			t.Fatal("malformed acl.yaml loaded without error")
		}
		e.DenyAll()
		if e.AllowPort("anyone", "tcp", 5432) {
			t.Fatal("traffic allowed with a broken ACL")
		}
		_ = os.WriteFile(path, []byte("default_policy: allow\n"), 0o600)
		if err := e.Reload(); err != nil || !e.AllowPort("anyone", "tcp", 5432) {
			t.Fatalf("reload after fixing the file: %v", err)
		}
	})
}
