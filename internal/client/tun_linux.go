//go:build linux

package client

// configureTUN on Linux is a no-op — the IP was already set by tunnel.NewTUN.
func configureTUN(ifaceName, localIP, peerIP string) error {
	return nil
}
