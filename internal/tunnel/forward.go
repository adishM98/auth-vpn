package tunnel

import (
	"fmt"
	"os"
	"strings"
)

// enableIPForward turns on IPv4 forwarding through its proc file. It reads
// first and only writes when forwarding is off: pods usually inherit it from
// the node, and /proc/sys is often read-only inside containers, so an
// unconditional write would warn about a setting that's already on.
func enableIPForward(path string) error {
	if b, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(b)) == "1" {
		return nil
	}
	if err := os.WriteFile(path, []byte("1"), 0o644); err != nil {
		return fmt.Errorf("IPv4 forwarding is off and can't be enabled (%v): enable net.ipv4.ip_forward on the host/node, or run the container privileged", err)
	}
	return nil
}
