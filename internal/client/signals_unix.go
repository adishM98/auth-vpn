//go:build !windows

package client

import (
	"os"
	"syscall"
)

// shutdownSignals are the OS signals that trigger a clean disconnect.
var shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
