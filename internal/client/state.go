package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// TunnelMeta is written to ~/.auth-vpn/tunnel.json when a tunnel comes up
// and deleted on clean shutdown. Used by status, disconnect and switching.
type TunnelMeta struct {
	PID         int       `json:"pid"`
	Profile     string    `json:"profile,omitempty"`
	Proxy       bool      `json:"proxy,omitempty"`
	ServerAddr  string    `json:"server_addr"`
	AssignedIP  string    `json:"assigned_ip"`
	ServerIP    string    `json:"server_ip"`
	Subnet      string    `json:"subnet"`
	ConnectedAt time.Time `json:"connected_at"`
}

// sudoUser returns the account that ran `sudo auth-vpn ...`, or nil when not
// running under sudo. TUN mode needs root, but profiles and pinned
// fingerprints belong to the developer, so they live in that user's home.
func sudoUser() *user.User {
	if os.Geteuid() != 0 {
		return nil
	}
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil
	}
	return u
}

func stateDir() string {
	if u := sudoUser(); u != nil {
		return filepath.Join(u.HomeDir, ".auth-vpn")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auth-vpn")
}

// writePrivate atomically writes a 0600 file in the state dir. Under sudo
// the dir and file are handed back to the invoking user, so a later
// `auth-vpn profile add` without sudo can still write them.
func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil {
		return err
	} else if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing to write", dir)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if u := sudoUser(); u != nil {
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		_ = os.Chown(dir, uid, gid)
		_ = os.Chown(tmp.Name(), uid, gid)
	}
	return os.Rename(tmp.Name(), path)
}

func metaFile() string { return filepath.Join(stateDir(), "tunnel.json") }

// WriteMeta atomically writes PID + connection info to disk.
// Fills meta.PID with the current process ID.
func WriteMeta(meta TunnelMeta) error {
	meta.PID = os.Getpid()
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return writePrivate(metaFile(), data)
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

// ClearMeta removes the tunnel metadata file, but only if it still describes
// this process: after a switch, the new tunnel owns the file.
func ClearMeta() error {
	if m, err := ReadMeta(); err == nil && m.PID != 0 && m.PID != os.Getpid() {
		return nil
	}
	err := os.Remove(metaFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ForceClearMeta removes the state file regardless of owner (stale cleanup).
func ForceClearMeta() error {
	err := os.Remove(metaFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// IsProcessAlive returns true if the given PID is a running process. EPERM
// means it exists but belongs to someone else, as when `auth-vpn status`
// runs without sudo against a tunnel started with sudo.
func IsProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

// StopRunningTunnel ends the TUN tunnel recorded in tunnel.json, if one is
// alive, and waits for it to tear down its routes. Two TUN tunnels can't run
// side by side (every server hands out the same 10.8.0.0/24 by default), so
// connecting to another server replaces the current one. Proxy-mode tunnels
// don't touch routes and are left alone. Returns what was stopped.
func StopRunningTunnel() (TunnelMeta, bool, error) {
	m, ok := CurrentTunnel()
	if !ok || m.Proxy {
		return TunnelMeta{}, false, nil
	}
	proc, err := os.FindProcess(m.PID)
	if err != nil {
		return m, false, err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return m, false, fmt.Errorf("stop running tunnel (pid %d): %w", m.PID, err)
	}
	for i := 0; i < 100 && IsProcessAlive(m.PID); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if IsProcessAlive(m.PID) {
		return m, false, fmt.Errorf("running tunnel (pid %d) did not exit within 10s", m.PID)
	}
	return m, true, nil
}

// CurrentTunnel returns the running tunnel: the one in tunnel.json, or else
// one started by an auth-vpn from before profile switching, which either
// wrote no state file (foreground) or wrote it to root's home under sudo.
func CurrentTunnel() (TunnelMeta, bool) {
	if m, err := ReadMeta(); err == nil && m.PID != os.Getpid() && IsProcessAlive(m.PID) {
		return m, true
	}
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return TunnelMeta{}, false // no ps (Windows): nothing more to find
	}
	return findConnectProcess(string(out), os.Getpid())
}

// findConnectProcess picks an `auth-vpn connect` process out of ps output.
// Only the auth-vpn binary itself counts (not the sudo or shell wrapping
// it), and a --forward command line is a proxy tunnel.
// ponytail: first match wins; two untracked TUN tunnels were already broken.
func findConnectProcess(ps string, self int) (TunnelMeta, bool) {
	for _, line := range strings.Split(ps, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || filepath.Base(f[1]) != "auth-vpn" || f[2] != "connect" {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil || pid == self {
			continue
		}
		m := TunnelMeta{PID: pid}
		for _, a := range f[3:] {
			if strings.HasPrefix(a, "--forward") {
				m.Proxy = true
			}
		}
		if len(f) > 3 && !strings.HasPrefix(f[3], "-") {
			m.ServerAddr = f[3]
		}
		return m, true
	}
	return TunnelMeta{}, false
}
