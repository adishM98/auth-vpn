package client

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type profilesFile struct {
	Profiles []Profile `yaml:"profiles"`
}

func profilesPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".auth-vpn", "profiles.yaml")
}

// SaveProfile writes a named connection profile to ~/.auth-vpn/profiles.yaml.
func SaveProfile(p Profile) error {
	path := profilesPath()
	pf := loadProfilesFile(path)

	// Replace if name already exists.
	replaced := false
	for i, existing := range pf.Profiles {
		if existing.Name == p.Name {
			pf.Profiles[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		pf.Profiles = append(pf.Profiles, p)
	}

	return saveProfilesFile(path, pf)
}

// LoadProfile looks up a profile by name.
func LoadProfile(name string) (Profile, error) {
	pf := loadProfilesFile(profilesPath())
	for _, p := range pf.Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("profile %q not found", name)
}

// ListProfiles returns all saved profiles.
func ListProfiles() []Profile {
	return loadProfilesFile(profilesPath()).Profiles
}

func loadProfilesFile(path string) profilesFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return profilesFile{}
	}
	var pf profilesFile
	_ = yaml.Unmarshal(data, &pf)
	return pf
}

func saveProfilesFile(path string, pf profilesFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(pf)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
