//go:build windows

package tunnel

import (
	"fmt"
	"os/exec"
	"strings"

	"golang.zx2c4.com/wireguard/tun"
)

const tunMTU = 1500

// Iface wraps a Wintun TUN device with a simple Read/Write/Close interface.
type Iface struct {
	dev  tun.Device
	name string
}

func (i *Iface) Read(buf []byte) (int, error) {
	bufs := [][]byte{buf}
	sizes := []int{len(buf)}
	n, err := i.dev.Read(bufs, sizes, 0)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	return sizes[0], nil
}

func (i *Iface) Write(buf []byte) (int, error) {
	_, err := i.dev.Write([][]byte{buf}, 0)
	if err != nil {
		return 0, err
	}
	return len(buf), nil
}

func (i *Iface) Close() error { return i.dev.Close() }
func (i *Iface) Name() string  { return i.name }

// NewTUN creates a Wintun TUN interface and assigns the given IP/mask.
// ip is in CIDR notation, e.g. "10.0.0.2/24".
// Requires wintun.dll alongside the executable (download from https://www.wintun.net/).
func NewTUN(ip string) (*Iface, error) {
	dev, err := tun.CreateTUN("auth-vpn0", tunMTU)
	if err != nil {
		return nil, fmt.Errorf("create tun: %w (ensure wintun.dll is in the same directory as auth-vpn.exe)", err)
	}

	name, err := dev.Name()
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("get tun name: %w", err)
	}

	// Drain device events so the internal channel never blocks.
	go func() {
		for range dev.Events() {
		}
	}()

	host, mask, err := parseCIDR(ip)
	if err != nil {
		dev.Close()
		return nil, err
	}

	out, err := exec.Command("netsh", "interface", "ip", "set", "address",
		name, "static", host, mask).CombinedOutput()
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("set ip (%s): %s", err, out)
	}

	return &Iface{dev: dev, name: name}, nil
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
