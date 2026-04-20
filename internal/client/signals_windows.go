//go:build windows

package client

import "os"

// shutdownSignals are the OS signals that trigger a clean disconnect.
// Windows doesn't support SIGTERM, so only os.Interrupt is used.
var shutdownSignals = []os.Signal{os.Interrupt}
