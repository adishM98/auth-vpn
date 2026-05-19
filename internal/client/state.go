package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// TunnelMeta is written to ~/.auth-vpn/tunnels/<server>.json when a tunnel
// comes up in --background mode and deleted on clean shutdown.
type TunnelMeta struct {
	PID         int       `json:"pid"`
	ServerAddr  string    `json:"server_addr"`
	AssignedIP  string    `json:"assigned_ip"`
	ServerIP    string    `json:"server_ip"`
	Subnet      string    `json:"subnet"`
	ConnectedAt time.Time `json:"connected_at"`
}

func stateDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auth-vpn")
}

func tunnelsDir() string {
	return filepath.Join(stateDir(), "tunnels")
}

// tunnelFile maps a server address (host:port) to a safe filename.
func tunnelFile(serverAddr string) string {
	safe := strings.ReplaceAll(serverAddr, ":", "_")
	return filepath.Join(tunnelsDir(), safe+".json")
}

// WriteMeta atomically writes PID + connection info for a specific server.
func WriteMeta(meta TunnelMeta) error {
	if err := os.MkdirAll(tunnelsDir(), 0o700); err != nil {
		return err
	}
	meta.PID = os.Getpid()
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(tunnelFile(meta.ServerAddr), data, 0o600)
}

// ClearMeta removes the state file for a specific server.
func ClearMeta(serverAddr string) error {
	err := os.Remove(tunnelFile(serverAddr))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ReadAllMeta reads all tunnel state files from ~/.auth-vpn/tunnels/.
// Returns an empty slice (not an error) when no tunnels are found.
func ReadAllMeta() ([]TunnelMeta, error) {
	entries, err := os.ReadDir(tunnelsDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []TunnelMeta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(tunnelsDir(), e.Name()))
		if err != nil {
			continue
		}
		var m TunnelMeta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		result = append(result, m)
	}
	return result, nil
}

// IsProcessAlive returns true if the given PID is a running process.
func IsProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
