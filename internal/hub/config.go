package hub

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ServerEntry describes a registered auth-vpn server managed by the hub.
type ServerEntry struct {
	Name           string `yaml:"name"`
	URL            string `yaml:"url"`
	APIKey         string `yaml:"api_key"`
	TLSFingerprint string `yaml:"tls_fingerprint,omitempty"`
}

// Config is the hub configuration (~/.auth-vpn/hub.yaml).
type Config struct {
	HubKey  string        `yaml:"hub_key,omitempty"`
	Servers []ServerEntry `yaml:"servers"`
}

// ConfigPath returns the path to the hub config file.
func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auth-vpn", "hub.yaml")
}

// LoadConfig reads hub configuration from disk. Returns an empty config if
// the file does not exist yet.
func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(ConfigPath())
	if os.IsNotExist(err) {
		return &Config{Servers: []ServerEntry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Servers == nil {
		cfg.Servers = []ServerEntry{}
	}
	return &cfg, nil
}

func saveConfig(cfg *Config) error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// snapshotConfig returns a deep copy of the config (safe to use outside cfgMu).
func snapshotConfig(cfg *Config) Config {
	snap := Config{HubKey: cfg.HubKey}
	snap.Servers = make([]ServerEntry, len(cfg.Servers))
	copy(snap.Servers, cfg.Servers)
	return snap
}
