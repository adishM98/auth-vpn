//go:build linux

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
// ip is in CIDR notation, e.g. "10.0.0.1/24".
func NewTUN(ip string) (*Iface, error) {
	cfg := water.Config{DeviceType: water.TUN}
	iface, err := water.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("create tun: %w", err)
	}

	name := iface.Name()

	if out, err := exec.Command("ip", "addr", "add", ip, "dev", name).CombinedOutput(); err != nil {
		_ = iface.Close()
		return nil, fmt.Errorf("set ip (%s): %s", err, out)
	}
	if out, err := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); err != nil {
		_ = iface.Close()
		return nil, fmt.Errorf("bring up (%s): %s", err, out)
	}

	return &Iface{iface}, nil
}

// AddRoute adds a host route through the TUN interface.
// subnet e.g. "10.0.0.0/24"
func AddRoute(subnet, ifaceName string) error {
	out, err := exec.Command("ip", "route", "replace", subnet, "dev", ifaceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("add route (%s): %s", err, out)
	}
	return nil
}

// DelRoute removes a route.
func DelRoute(subnet string) error {
	out, err := exec.Command("ip", "route", "del", subnet).CombinedOutput()
	if err != nil {
		return fmt.Errorf("del route (%s): %s", err, out)
	}
	return nil
}

// EnableForwarding turns on kernel IP forwarding (needed on server).
func EnableForwarding() error {
	out, err := exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput()
	if err != nil {
		return fmt.Errorf("enable forwarding (%s): %s", err, out)
	}
	return nil
}
