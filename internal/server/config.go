package server

import (
	"os"

	"gopkg.in/yaml.v3"
)

const (
	ServerConfigFile = "/etc/auth-vpn/server.yaml"
	ACLFile          = "/etc/auth-vpn/acl.yaml"
	DefaultMetricsAddr = "127.0.0.1:9100"
)

// ServerConfig is the schema for /etc/auth-vpn/server.yaml.
type ServerConfig struct {
	Port        int    `yaml:"port"`
	Subnet      string `yaml:"subnet"`
	ServerIP    string `yaml:"server_ip"`
	MetricsAddr string `yaml:"metrics_addr"`
	ACLPath     string `yaml:"acl_path,omitempty"`
	APIKey      string `yaml:"api_key,omitempty"`
}

// DefaultServerConfig returns a ServerConfig with sensible defaults.
func DefaultServerConfig(port int) ServerConfig {
	return ServerConfig{
		Port:        port,
		Subnet:      "10.8.0.0/24",
		ServerIP:    "10.8.0.1",
		MetricsAddr: DefaultMetricsAddr,
		ACLPath:     ACLFile,
	}
}

// LoadServerConfig reads and parses a server.yaml file.
func LoadServerConfig(path string) (ServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ServerConfig{}, err
	}
	var cfg ServerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ServerConfig{}, err
	}
	return cfg, nil
}

// SaveServerConfig writes a ServerConfig to disk.
func SaveServerConfig(path string, cfg ServerConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
