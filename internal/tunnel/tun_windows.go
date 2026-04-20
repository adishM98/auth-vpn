//go:build windows

package tunnel

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/songgao/water"
)

// Iface wraps a TUN interface.
type Iface struct {
	*water.Interface
}

// NewTUN creates a TUN interface and assigns the given IP/mask.
// ip is in CIDR notation, e.g. "10.0.0.2/24".
func NewTUN(ip string) (*Iface, error) {
	cfg := water.Config{DeviceType: water.TUN}
	iface, err := water.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("create tun: %w", err)
	}

	// Parse IP and prefix for netsh.
	host, mask, err := parseCIDR(ip)
	if err != nil {
		_ = iface.Close()
		return nil, err
	}

	name := iface.Name()
	out, err := exec.Command("netsh", "interface", "ip", "set", "address",
		name, "static", host, mask).CombinedOutput()
	if err != nil {
		_ = iface.Close()
		return nil, fmt.Errorf("set ip (%s): %s", err, out)
	}

	return &Iface{iface}, nil
}

// AddRoute adds a route through the TUN interface.
func AddRoute(subnet, ifaceName string) error {
	host, mask, err := parseCIDR(subnet)
	if err != nil {
		return err
	}
	out, err := exec.Command("route", "add", host, "mask", mask, "0.0.0.0",
		"if", ifaceName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("add route (%s): %s", err, out)
	}
	return nil
}

// DelRoute removes a route.
func DelRoute(subnet string) error {
	host, _, err := parseCIDR(subnet)
	if err != nil {
		return err
	}
	out, err := exec.Command("route", "delete", host).CombinedOutput()
	if err != nil {
		return fmt.Errorf("del route (%s): %s", err, out)
	}
	return nil
}

// EnableForwarding is a no-op on the Windows client.
func EnableForwarding() error { return nil }

// parseCIDR splits "10.0.0.2/24" into host "10.0.0.2" and mask "255.255.255.0".
func parseCIDR(cidr string) (host, mask string, err error) {
	parts := strings.SplitN(cidr, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid CIDR %q", cidr)
	}
	host = parts[0]
	prefixLen := 0
	if _, e := fmt.Sscanf(parts[1], "%d", &prefixLen); e != nil {
		return "", "", fmt.Errorf("invalid prefix in %q", cidr)
	}
	bits := ^uint32(0) << (32 - prefixLen)
	mask = fmt.Sprintf("%d.%d.%d.%d",
		(bits>>24)&0xff, (bits>>16)&0xff, (bits>>8)&0xff, bits&0xff)
	return host, mask, nil
}
