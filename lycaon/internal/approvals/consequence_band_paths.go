package approvals

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/fspath"
)

// ConsequenceBandPaths holds canonical high-impact destinations.
type ConsequenceBandPaths struct {
	entries []string
	home    string
}

// UserConsequenceBandPath returns {configDir}/consequence-band.yaml (device layer).
func UserConsequenceBandPath(configDir string) string {
	return filepath.Join(configDir, "consequence-band.yaml")
}

type consequenceBandFile struct {
	Paths map[string][]string `yaml:"consequence_band_paths"`
}

// LoadConsequenceBandPaths reads and normalizes the shipped consequence-band catalog.
func LoadConsequenceBandPaths() (ConsequenceBandPaths, error) {
	data, err := config.Read(config.ConsequenceBandPaths)
	if err != nil {
		return ConsequenceBandPaths{}, fmt.Errorf("read consequence-band paths: %w", err)
	}
	return parseConsequenceBandPathsYAML(data)
}

// LoadMergedConsequenceBandPaths merges host then device layers. An empty
// deviceConfigDir skips the device layer. There is no project layer.
func LoadMergedConsequenceBandPaths(deviceConfigDir string) (ConsequenceBandPaths, error) {
	host, err := LoadConsequenceBandPaths()
	if err != nil {
		return ConsequenceBandPaths{}, err
	}
	dir := strings.TrimSpace(deviceConfigDir)
	if dir == "" {
		return host, nil
	}
	// The device layer is a host file, so it is read as one. Calling the bundled
	// loader here would re-read the shipped catalog and make the merge a no-op.
	raw, err := os.ReadFile(UserConsequenceBandPath(dir)) // #nosec G304 -- device config dir
	if err != nil {
		if os.IsNotExist(err) {
			return host, nil
		}
		return ConsequenceBandPaths{}, fmt.Errorf("read device consequence-band paths: %w", err)
	}
	device, err := parseConsequenceBandPathsYAML(raw)
	if err != nil {
		return ConsequenceBandPaths{}, err
	}
	return mergeConsequenceBandPaths(host, device), nil
}

func mergeConsequenceBandPaths(host, device ConsequenceBandPaths) ConsequenceBandPaths {
	seen := make(map[string]struct{}, len(host.entries)+len(device.entries))
	out := make([]string, 0, len(host.entries)+len(device.entries))
	add := func(entries []string) {
		for _, e := range entries {
			if _, ok := seen[e]; ok {
				continue
			}
			seen[e] = struct{}{}
			out = append(out, e)
		}
	}
	add(host.entries)
	add(device.entries)
	home := host.home
	if home == "" {
		home = device.home
	}
	return ConsequenceBandPaths{entries: out, home: home}
}

func parseConsequenceBandPathsYAML(data []byte) (ConsequenceBandPaths, error) {
	var file consequenceBandFile
	if err := config.DecodeYAML(data, &file); err != nil {
		return ConsequenceBandPaths{}, fmt.Errorf("parse consequence-band paths: %w", err)
	}
	if len(file.Paths) == 0 {
		return ConsequenceBandPaths{}, fmt.Errorf("consequence_band_paths required")
	}
	home, _ := os.UserHomeDir()
	var paths []string
	for _, group := range file.Paths {
		for _, raw := range group {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			p, err := normalizeDestinationPath(raw, home)
			if err != nil {
				return ConsequenceBandPaths{}, fmt.Errorf("sensitive destination %q: %w", raw, err)
			}
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return ConsequenceBandPaths{}, fmt.Errorf("consequence_band_paths: at least one path required")
	}
	return ConsequenceBandPaths{entries: paths, home: home}, nil
}

func normalizeDestinationPath(raw, home string) (string, error) {
	p, err := expandHomePath(raw, home)
	if err != nil {
		return "", err
	}
	return fspath.CanonicalPath(p), nil
}

func expandHomePath(raw, home string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", fmt.Errorf("empty path")
	case raw == "~":
		if home == "" {
			return "", fmt.Errorf("home directory unavailable")
		}
		return home, nil
	case strings.HasPrefix(raw, "~/"):
		if home == "" {
			return "", fmt.Errorf("home directory unavailable")
		}
		return filepath.Join(home, raw[2:]), nil
	default:
		return filepath.Clean(raw), nil
	}
}

// Intersects reports whether root is at, under, or an ancestor of a catalog entry.
// Comparison uses path segments, not string prefixes.
func (d ConsequenceBandPaths) Intersects(root string) bool {
	root = strings.TrimSpace(root)
	if root == "" {
		return false
	}
	p, err := expandHomePath(root, d.home)
	if err != nil {
		return false
	}
	p = fspath.CanonicalPath(p)
	rootSegs := pathSegments(p)
	if len(rootSegs) == 0 {
		return false
	}
	for _, entry := range d.entries {
		if segmentsIntersect(rootSegs, pathSegments(entry)) {
			return true
		}
	}
	return false
}

func pathSegments(p string) []string {
	p = filepath.ToSlash(filepath.Clean(p))
	if p == "" || p == "." {
		return nil
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func segmentsIntersect(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	shorter, longer := a, b
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	for i, seg := range shorter {
		if !strings.EqualFold(seg, longer[i]) {
			return false
		}
	}
	return true
}
