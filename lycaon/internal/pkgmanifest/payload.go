package pkgmanifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

// PayloadEntry seals one app-managed object relative to the logical install root.
// Inapplicable file and link fields are null, rather than alternate read shapes.
type PayloadEntry struct {
	Path   string  `json:"path"`
	Kind   string  `json:"kind"`
	Mode   uint32  `json:"mode"`
	SHA256 *string `json:"sha256"`
	Size   *int64  `json:"size"`
	Target *string `json:"target"`
}

// UnmarshalJSON requires the complete tagged record, including explicit nulls.
func (entry *PayloadEntry) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if len(fields) != 6 {
		return errors.New("native manifest: incomplete payload record")
	}
	for _, key := range []string{"path", "kind", "mode", "sha256", "size", "target"} {
		if _, ok := fields[key]; !ok {
			return errors.New("native manifest: unsupported payload record")
		}
	}
	type plain PayloadEntry
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*entry = PayloadEntry(decoded)
	return nil
}

func (m *Manifest) validatePayload() error {
	if len(m.Payload) == 0 {
		return errors.New("native manifest: payload is empty")
	}
	entries := make(map[string]PayloadEntry, len(m.Payload))
	for _, entry := range m.Payload {
		if entry.Path == "" || entry.Path == "." || path.IsAbs(entry.Path) || path.Clean(entry.Path) != entry.Path || strings.HasPrefix(entry.Path, "../") || entry.Path == "manifest.json" {
			return fmt.Errorf("native manifest: invalid payload path %q", entry.Path)
		}
		if _, ok := entries[entry.Path]; ok {
			return fmt.Errorf("native manifest: duplicate payload path %q", entry.Path)
		}
		if entry.Mode > 0777 || entry.Kind != "symlink" && entry.Mode&0022 != 0 {
			return fmt.Errorf("native manifest: unsafe payload mode for %q", entry.Path)
		}
		switch entry.Kind {
		case "file":
			if entry.SHA256 == nil || !isLowerHexDigest(*entry.SHA256) || entry.Size == nil || *entry.Size < 0 || entry.Target != nil {
				return fmt.Errorf("native manifest: invalid payload file %q", entry.Path)
			}
		case "directory":
			if entry.SHA256 != nil || entry.Size != nil || entry.Target != nil || entry.Mode&0500 != 0500 {
				return fmt.Errorf("native manifest: invalid payload directory %q", entry.Path)
			}
		case "symlink":
			if entry.Mode != 0777 || entry.SHA256 != nil || entry.Size != nil || entry.Target == nil || *entry.Target == "" || path.IsAbs(*entry.Target) {
				return fmt.Errorf("native manifest: invalid payload link %q", entry.Path)
			}
			resolved := path.Clean(path.Join(path.Dir(entry.Path), *entry.Target))
			if resolved == ".." || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("native manifest: escaping payload link %q", entry.Path)
			}
		default:
			return fmt.Errorf("native manifest: unknown payload kind %q", entry.Kind)
		}
		entries[entry.Path] = entry
	}
	for name := range entries {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if entries[parent].Kind != "directory" {
				return fmt.Errorf("native manifest: missing physical payload parent %q", parent)
			}
		}
	}
	for _, entry := range entries {
		if entry.Kind != "symlink" {
			continue
		}
		selected := entry.Path
		visited := map[string]bool{}
		for entries[selected].Kind == "symlink" {
			if visited[selected] {
				return fmt.Errorf("native manifest: cyclic payload link %q", entry.Path)
			}
			visited[selected] = true
			link := entries[selected]
			selected = path.Clean(path.Join(path.Dir(selected), *link.Target))
			if selected == "." {
				break
			}
			if _, ok := entries[selected]; !ok {
				return fmt.Errorf("native manifest: dangling payload link %q", entry.Path)
			}
		}
	}
	for _, exe := range m.Executables {
		if !strings.HasPrefix(exe.Path, m.InstallRoot+"/") {
			continue
		}
		entry, ok := entries[strings.TrimPrefix(exe.Path, m.InstallRoot+"/")]
		if !ok || entry.Kind != "file" || entry.Mode&0111 == 0 || *entry.SHA256 != exe.SHA256 || *entry.Size != exe.Size {
			return fmt.Errorf("native manifest: executable differs from payload inventory %q", exe.Path)
		}
	}
	return nil
}
