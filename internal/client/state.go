package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// TunnelMeta is written to ~/.auth-vpn/tunnel.json when a tunnel comes up
// and deleted on clean shutdown. Used by status and disconnect commands.
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

func metaFile() string { return filepath.Join(stateDir(), "tunnel.json") }

// WriteMeta atomically writes PID + connection info to disk.
// Fills meta.PID with the current process ID.
func WriteMeta(meta TunnelMeta) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	meta.PID = os.Getpid()
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(metaFile(), data, 0o600)
}

// ReadMeta reads and decodes the tunnel metadata file.
// Returns os.ErrNotExist if no tunnel state file is present.
func ReadMeta() (TunnelMeta, error) {
	data, err := os.ReadFile(metaFile())
	if err != nil {
		return TunnelMeta{}, err
	}
	var m TunnelMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return TunnelMeta{}, err
	}
	return m, nil
}

// ClearMeta removes the tunnel metadata file.
func ClearMeta() error {
	err := os.Remove(metaFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// IsProcessAlive returns true if the given PID is a running process.
func IsProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
