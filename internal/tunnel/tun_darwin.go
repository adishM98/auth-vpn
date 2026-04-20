//go:build darwin

package tunnel

import (
	"fmt"
	"os/exec"

	"github.com/songgao/water"
)

// Iface wraps a TUN interface.
type Iface struct {
	*water.Interface
}

// NewTUN creates a TUN interface and assigns the given IP address.
// ip is the local address, peer is the remote endpoint (server IP).
// On macOS, TUN uses point-to-point addressing: ifconfig utunX <local> <peer> up
func NewTUN(ip string) (*Iface, error) {
	cfg := water.Config{DeviceType: water.TUN}
	iface, err := water.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("create tun: %w", err)
	}
	return &Iface{iface}, nil
}

// ConfigureTUN sets point-to-point addresses on a macOS utun interface.
// localIP e.g. "10.0.0.2", peerIP e.g. "10.0.0.1"
func ConfigureTUN(ifaceName, localIP, peerIP string) error {
	out, err := exec.Command("ifconfig", ifaceName, localIP, peerIP, "up").CombinedOutput()
	if err != nil {
		return fmt.Errorf("ifconfig (%s): %s", err, out)
	}
	return nil
}

// AddRoute adds a route through the TUN interface.
func AddRoute(subnet, ifaceName string) error {
	out, err := exec.Command("route", "add", "-net", subnet, "-interface", ifaceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("add route (%s): %s", err, out)
	}
	return nil
}

// DelRoute removes a route.
func DelRoute(subnet string) error {
	out, err := exec.Command("route", "delete", "-net", subnet).CombinedOutput()
	if err != nil {
		return fmt.Errorf("del route (%s): %s", err, out)
	}
	return nil
}

// EnableForwarding is a no-op on macOS for client use.
func EnableForwarding() error { return nil }
