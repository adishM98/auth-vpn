//go:build windows

package client

// configureTUN is a no-op on Windows; the IP is set by tunnel.NewTUN.
func configureTUN(ifaceName, localIP, peerIP string) error { return nil }
