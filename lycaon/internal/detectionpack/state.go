package detectionpack

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fseffect"
	"gopkg.in/yaml.v3"
)

type deviceStateFile struct {
	Disabled []string `yaml:"disabled"`
}

// DisabledIDs returns the pack ids this device turned off, including ids no
// pack currently provides: a decision to silence something outlives the
// provider being uninstalled, updated, or temporarily unresolvable.
func DisabledIDs(configDir string) (map[string]struct{}, error) {
	raw, err := loadDisabledIDs(configDir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(raw))
	for id := range raw {
		out[id] = struct{}{}
	}
	return out, nil
}

func loadDisabledIDs(configDir string) (map[string]bool, error) {
	out := map[string]bool{}
	if configDir == "" {
		return out, nil
	}
	path := DeviceStatePath(configDir)
	data, err := os.ReadFile(path) // #nosec G304 -- device state under configDir
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	var raw deviceStateFile
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return out, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, id := range raw.Disabled {
		if id != "" {
			out[id] = true
		}
	}
	return out, nil
}

// WriteDisabledIDs writes the device disabled-pack state file.
func WriteDisabledIDs(configDir string, disabled []string) error {
	if configDir == "" {
		return fmt.Errorf("configDir required")
	}
	seen := make(map[string]struct{}, len(disabled))
	canonical := make([]string, 0, len(disabled))
	for _, id := range disabled {
		id = strings.TrimSpace(id)
		if !packIDPattern.MatchString(id) {
			return fmt.Errorf("invalid detection pack id %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		canonical = append(canonical, id)
	}
	sort.Strings(canonical)
	raw := deviceStateFile{Disabled: canonical}
	data, err := yaml.Marshal(&raw)
	if err != nil {
		return err
	}
	path := DeviceStatePath(configDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(path),
		Source:   bytes.NewReader(data),
		Mode:     0o600,
		DirMode:  0o700,
	})
	return err
}
