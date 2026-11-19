package linuxsandbox

import (
	"path"
	"slices"
	"strings"
)

// PrivateTempMounts carries the backing directory and explicit host subtrees
// that keep their identity inside a sandbox temp directory.
type PrivateTempMounts struct {
	Paths    []string `json:"paths"`
	Scratch  string   `json:"scratch,omitempty"`
	Retained []string `json:"retained,omitempty"`
}

// PrivateTempPaths are the mount destinations supplied by the Linux adapter.
func PrivateTempPaths() []string { return []string{"/tmp", "/var/tmp"} }

func (m *PrivateTempMounts) validate() error {
	if m == nil {
		return nil
	}
	if !slices.Equal(m.Paths, PrivateTempPaths()) ||
		(m.Scratch != "" && (!path.IsAbs(m.Scratch) || path.Clean(m.Scratch) != m.Scratch || m.Scratch == "/" || slices.Contains(m.Paths, m.Scratch))) {
		return &Unsupported{Code: UnsupportedFilesystemRule, Detail: "invalid private temp mapping"}
	}
	for _, p := range m.Retained {
		inside := slices.ContainsFunc(m.Paths, func(alias string) bool { return strings.HasPrefix(p, alias+"/") })
		if path.Clean(p) != p || !inside {
			return &Unsupported{Code: UnsupportedFilesystemRule, Detail: "invalid private temp subtree"}
		}
	}
	return nil
}

// The performer judges a pinned name against its backing folder's floors.
func (m *PrivateTempMounts) policyPath(name string) string {
	if m == nil || m.Scratch == "" {
		return name
	}
	for _, root := range m.Retained {
		if name == root || strings.HasPrefix(name, root+"/") {
			return name
		}
	}
	for _, alias := range m.Paths {
		if name == alias {
			return m.Scratch
		}
		if rel, inside := strings.CutPrefix(name, alias+"/"); inside {
			return path.Join(m.Scratch, rel)
		}
	}
	return name
}
