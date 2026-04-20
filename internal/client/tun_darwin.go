//go:build darwin

package client

import "github.com/adishM98/auth-vpn/internal/tunnel"

// configureTUN on macOS sets the point-to-point addresses on the utun interface.
func configureTUN(ifaceName, localIP, peerIP string) error {
	return tunnel.ConfigureTUN(ifaceName, localIP, peerIP)
}
