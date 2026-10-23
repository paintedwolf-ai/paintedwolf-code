package packagegeneration

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

func (seal *RuntimeSeal) validateFiles() (map[string]RuntimeFile, error) {
	files := make(map[string]RuntimeFile, len(seal.Files))
	for _, file := range seal.Files {
		if !runtimeRelative(file.Path) || file.Mode > 0777 || file.Kind != "symlink" && file.Mode&0022 != 0 {
			return nil, fmt.Errorf("runtime seal: unsafe final object %q", file.Path)
		}
		if _, duplicate := files[file.Path]; duplicate {
			return nil, fmt.Errorf("runtime seal: duplicate final object %q", file.Path)
		}
		switch file.Kind {
		case "file":
			if file.SHA256 == nil || !runtimeHash(*file.SHA256) || file.Size == nil || *file.Size < 0 || file.Target != nil {
				return nil, fmt.Errorf("runtime seal: invalid final file %q", file.Path)
			}
		case "directory":
			if file.SHA256 != nil || file.Size != nil || file.Target != nil || file.Mode&0500 != 0500 {
				return nil, fmt.Errorf("runtime seal: invalid final directory %q", file.Path)
			}
		case "symlink":
			if file.Mode != 0777 || file.SHA256 != nil || file.Size != nil || file.Target == nil ||
				!runtimeText(*file.Target, 4095) || path.IsAbs(*file.Target) || path.Clean(*file.Target) != *file.Target {
				return nil, fmt.Errorf("runtime seal: invalid final link %q", file.Path)
			}
		default:
			return nil, errors.New("runtime seal: unsupported final object kind")
		}
		files[file.Path] = file
	}
	for name, file := range files {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if files[parent].Kind != "directory" {
				return nil, fmt.Errorf("runtime seal: missing physical parent %q", parent)
			}
		}
		if file.Kind == "symlink" {
			if _, err := resolveRuntimePath(name, files); err != nil {
				return nil, err
			}
		}
	}
	return files, nil
}

// Resolve links component by component, including a link in a target's parent.
// Physical file entries themselves may never sit beneath a symlink.
func resolveRuntimePath(name string, files map[string]RuntimeFile) (string, error) {
	for followed := 0; followed <= len(files); followed++ {
		if name == "." {
			return name, nil // the retained physical root
		}
		if !runtimeRelative(name) {
			return "", errors.New("runtime seal: escaping link")
		}
		parts := strings.Split(name, "/")
		changed := false
		for index := range parts {
			prefix := strings.Join(parts[:index+1], "/")
			file, ok := files[prefix]
			if !ok {
				return "", fmt.Errorf("runtime seal: dangling link target %q", name)
			}
			if file.Kind == "symlink" {
				name = path.Join(path.Dir(prefix), *file.Target)
				if index+1 < len(parts) {
					name = path.Join(name, strings.Join(parts[index+1:], "/"))
				}
				changed = true
				break
			}
			if index+1 < len(parts) && file.Kind != "directory" {
				return "", errors.New("runtime seal: link traverses regular file")
			}
		}
		if !changed {
			return name, nil
		}
	}
	return "", errors.New("runtime seal: cyclic link")
}

func (seal *RuntimeSeal) validateAliases(files map[string]RuntimeFile) error {
	aliases := make(map[string]RuntimeAlias, len(seal.Aliases))
	for _, alias := range seal.Aliases {
		file, ok := files[alias.Target]
		if !runtimeAbsolute(alias.Path) || !ok || file.Kind == "symlink" || alias.Kind != file.Kind {
			return fmt.Errorf("runtime seal: invalid projection %q", alias.Path)
		}
		if _, duplicate := aliases[alias.Path]; duplicate {
			return fmt.Errorf("runtime seal: duplicate projection %q", alias.Path)
		}
		aliases[alias.Path] = alias
	}
	for name, alias := range aliases {
		for parent := path.Dir(name); parent != "/"; parent = path.Dir(parent) {
			ancestor, exists := aliases[parent]
			if !exists {
				continue
			}
			if ancestor.Kind != "directory" ||
				path.Join(ancestor.Target, strings.TrimPrefix(name, parent+"/")) != alias.Target {
				return fmt.Errorf("runtime seal: conflicting nested projection %q", name)
			}
		}
	}
	return nil
}
