package extpacks

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Profile is profiles/<name>.yaml inside a pack.
type Profile struct {
	Name    string            `yaml:"name"`
	Enable  []string          `yaml:"enable"`
	Disable []string          `yaml:"disable"`
	Own     map[string]string `yaml:"own"`
}

// LoadProfile reads profiles/<name>.yaml from packRoot.
func LoadProfile(packRoot, name string) (Profile, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return Profile{}, fmt.Errorf("invalid profile name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(packRoot, "profiles", name+".yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return Profile{}, fmt.Errorf("%w: %s", ErrProfileNotFound, name)
	}
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Profile{}, fmt.Errorf("profile %s: multiple YAML documents are not allowed", name)
		}
		return Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	if strings.TrimSpace(p.Name) == "" {
		p.Name = name
	} else if strings.TrimSpace(p.Name) != name {
		return Profile{}, fmt.Errorf("profile %s: name must match the filename", name)
	}
	if p.Own == nil {
		p.Own = map[string]string{}
	}
	return p, nil
}

// ErrProfileNotFound names a profile the pack does not declare.
var ErrProfileNotFound = errors.New("extension profile not found")

// LoadDeviceProfile loads a profile from an installed pack. Stock packs
// declare no profiles.
func LoadDeviceProfile(packID, profileName string) (Profile, error) {
	if IsStockPackID(packID) {
		return Profile{}, fmt.Errorf("%w: %s", ErrProfileNotFound, profileName)
	}
	packRoot, err := resolvePackRoot(packID)
	if err != nil {
		return Profile{}, err
	}
	return LoadProfile(packRoot, profileName)
}

// ApplyTo merges the profile's enable/disable/own sets into desired state.
// Pack ids in disable flip the pack's enabled flag; unit ids join disabled.
func (p Profile) ApplyTo(d DesiredState) DesiredState {
	tr := true
	f := false
	for _, id := range p.Enable {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		d = upsertPackEnabled(d, id, &tr)
	}
	for _, id := range p.Disable {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if !isUnitID(id) {
			d = upsertPackEnabled(d, id, &f)
			continue
		}
		d.Disabled = appendUnique(d.Disabled, id)
	}
	if d.Own == nil {
		d.Own = map[string]string{}
	}
	for unit, pack := range p.Own {
		d.Own[unit] = pack
	}
	return d
}

func upsertPackEnabled(d DesiredState, packID string, enabled *bool) DesiredState {
	for i := range d.Packs {
		if d.Packs[i].ID == packID {
			d.Packs[i].Enabled = enabled
			return d
		}
	}
	d.Packs = append(d.Packs, DesiredPack{ID: packID, Enabled: enabled})
	return d
}

func resolvePackRoot(packID string) (string, error) {
	lockPath, err := DeviceLockPath()
	if err != nil {
		return "", err
	}
	lock, err := LoadLockFile(lockPath)
	if err != nil {
		return "", err
	}
	locked, ok := lock.Package(packID)
	if !ok {
		return "", fmt.Errorf("%w: %s missing from device lock", ErrPackNotInstalled, packID)
	}
	if locked.Kind == PackKindPath {
		src := strings.TrimSpace(locked.Source)
		if src == "" {
			return "", fmt.Errorf("%w: pack %s", ErrLinkedSourceMissing, packID)
		}
		abs, err := filepath.Abs(src)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(abs, "extension.yaml")); err != nil {
			return "", fmt.Errorf("%w: pack %s: %w", ErrLinkedSourceMissing, packID, err)
		}
		return abs, nil
	}
	dir, err := CachedPackRevisionDir(packID, locked.Revision)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "extension.yaml")); err != nil {
		return "", fmt.Errorf("%w: %s missing from cache", ErrPackNotInstalled, packID)
	}
	return dir, nil
}
