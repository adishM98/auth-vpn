package client

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrProfileNotFound is returned when no saved profile has the given name.
var ErrProfileNotFound = errors.New("profile not found")

// DefaultPort is the tunnel port used when a profile host has none.
const DefaultPort = "7777"

type profilesFile struct {
	Profiles []Profile `yaml:"profiles"`
}

func profilesPath() string { return filepath.Join(stateDir(), "profiles.yaml") }

// NormalizeProfile validates a profile and fills in the default port.
func NormalizeProfile(p Profile) (Profile, error) {
	if p.Name == "" || strings.ContainsAny(p.Name, ":/ \t") {
		return p, fmt.Errorf("invalid profile name %q (no spaces, ':' or '/')", p.Name)
	}
	if p.Host == "" {
		return p, fmt.Errorf("profile %q needs a server address", p.Name)
	}
	if p.Fingerprint != "" && !strings.HasPrefix(p.Fingerprint, "SHA256:") {
		return p, fmt.Errorf("fingerprint must look like SHA256:..., got %q", p.Fingerprint)
	}
	if _, _, err := net.SplitHostPort(p.Host); err != nil {
		p.Host = net.JoinHostPort(strings.Trim(p.Host, "[]"), DefaultPort)
	}
	return p, nil
}

// SaveProfile adds or replaces a named connection profile.
func SaveProfile(p Profile) error {
	p, err := NormalizeProfile(p)
	if err != nil {
		return err
	}
	pf, err := loadProfilesFile()
	if err != nil {
		return err
	}
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
	return saveProfilesFile(pf)
}

// RemoveProfile deletes a profile by name.
func RemoveProfile(name string) error {
	pf, err := loadProfilesFile()
	if err != nil {
		return err
	}
	kept := pf.Profiles[:0]
	for _, p := range pf.Profiles {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	if len(kept) == len(pf.Profiles) {
		return fmt.Errorf("%w: %q", ErrProfileNotFound, name)
	}
	pf.Profiles = kept
	return saveProfilesFile(pf)
}

// LoadProfile looks up a profile by name.
func LoadProfile(name string) (Profile, error) {
	pf, err := loadProfilesFile()
	if err != nil {
		return Profile{}, err
	}
	for _, p := range pf.Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("%w: %q", ErrProfileNotFound, name)
}

// ListProfiles returns all saved profiles.
func ListProfiles() ([]Profile, error) {
	pf, err := loadProfilesFile()
	return pf.Profiles, err
}

// IsPinned reports whether connecting to p verifies a known certificate,
// either from the profile itself or from a fingerprint trusted earlier.
func IsPinned(p Profile) bool {
	if p.Fingerprint != "" {
		return true
	}
	_, ok := loadKnownHosts().Hosts[p.Host]
	return ok
}

// A file that fails to parse is an error, not an empty list: saving over it
// would silently wipe every other profile.
func loadProfilesFile() (profilesFile, error) {
	path := profilesPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return profilesFile{}, nil
	}
	if err != nil {
		return profilesFile{}, err
	}
	var pf profilesFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		return profilesFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	return pf, nil
}

func saveProfilesFile(pf profilesFile) error {
	data, err := yaml.Marshal(pf)
	if err != nil {
		return err
	}
	return writePrivate(profilesPath(), data)
}
