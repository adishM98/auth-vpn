package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// WhitelistEntry is one IP or CIDR range that can connect without a token.
type WhitelistEntry struct {
	Name      string    `json:"name"`
	IP        string    `json:"ip"`         // single IP or CIDR e.g. "1.2.3.4" or "10.0.0.0/24"
	CreatedAt time.Time `json:"created_at"`
}

// WhitelistManager manages IP whitelist entries with thread-safe access and JSON persistence.
type WhitelistManager struct {
	path    string
	mu      sync.RWMutex
	entries []WhitelistEntry
}

// NewWhitelistManager loads the whitelist from path. Missing file is not an error.
func NewWhitelistManager(path string) (*WhitelistManager, error) {
	wm := &WhitelistManager{path: path, entries: []WhitelistEntry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return wm, nil
		}
		return nil, fmt.Errorf("read whitelist %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &wm.entries); err != nil {
		return nil, fmt.Errorf("parse whitelist: %w", err)
	}
	return wm, nil
}

func (wm *WhitelistManager) save() error {
	data, err := json.MarshalIndent(wm.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(wm.path, data, 0o600)
}

// Add adds a new entry. Returns error if name or IP already exists.
func (wm *WhitelistManager) Add(name, ip string) error {
	// validate: must be a parseable IP or CIDR
	if net.ParseIP(ip) == nil {
		if _, _, err := net.ParseCIDR(ip); err != nil {
			return fmt.Errorf("invalid IP or CIDR %q", ip)
		}
	}

	wm.mu.Lock()
	defer wm.mu.Unlock()

	for _, e := range wm.entries {
		if e.Name == name {
			return fmt.Errorf("entry %q already exists", name)
		}
		if e.IP == ip {
			return fmt.Errorf("IP %q already whitelisted as %q", ip, e.Name)
		}
	}

	wm.entries = append(wm.entries, WhitelistEntry{
		Name:      name,
		IP:        ip,
		CreatedAt: time.Now().UTC(),
	})
	return wm.save()
}

// Remove deletes an entry by name.
func (wm *WhitelistManager) Remove(name string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	for i, e := range wm.entries {
		if e.Name == name {
			wm.entries = append(wm.entries[:i], wm.entries[i+1:]...)
			return wm.save()
		}
	}
	return fmt.Errorf("entry %q not found", name)
}

// List returns a copy of all entries.
func (wm *WhitelistManager) List() []WhitelistEntry {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	out := make([]WhitelistEntry, len(wm.entries))
	copy(out, wm.entries)
	return out
}

// IPForName returns the IP/CIDR for the named entry, or "" if not found.
func (wm *WhitelistManager) IPForName(name string) string {
	wm.mu.RLock()
	defer wm.mu.RUnlock()
	for _, e := range wm.entries {
		if e.Name == name {
			return e.IP
		}
	}
	return ""
}

// Contains returns the entry name and true if remoteIP matches any entry.
// Supports both exact IP and CIDR range matching.
func (wm *WhitelistManager) Contains(remoteIP string) (string, bool) {
	ip := net.ParseIP(remoteIP)
	if ip == nil {
		return "", false
	}

	wm.mu.RLock()
	defer wm.mu.RUnlock()

	for _, e := range wm.entries {
		if entryIP := net.ParseIP(e.IP); entryIP != nil {
			if entryIP.Equal(ip) {
				return e.Name, true
			}
			continue
		}
		if _, cidr, err := net.ParseCIDR(e.IP); err == nil {
			if cidr.Contains(ip) {
				return e.Name, true
			}
		}
	}
	return "", false
}
