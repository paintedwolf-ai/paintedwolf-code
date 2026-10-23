//go:build linux

package packagegeneration

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
)

// Resolve a source link through authenticated descriptor-relative components.
// External alternatives directories are provenance, never projection authority.
func (l layout) resolveRuntimeLink(name string, origin RuntimeOrigin, snapshot *runtimeSnapshot) (string, error) {
	pending := name
	seen := map[string]bool{}
	var links []RuntimeLink
	for {
		relative, err := filepath.Rel(l.anchor, pending)
		if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
			return "", errors.New("runtime capture: link outside trust anchor")
		}
		current, err := l.runtimeSourceDirectory(l.anchor, snapshot.Custody)
		if err != nil {
			return "", err
		}
		parts := strings.Split(relative, string(os.PathSeparator))
		physical := l.anchor
		restarted := false
		for index, component := range parts {
			child, err := openRuntimeAt(current, component, unix.O_PATH)
			if err != nil {
				_ = current.Close()
				return "", err
			}
			physical = filepath.Join(physical, component)
			if err := l.recordRuntimeCustody(physical, child, snapshot.Custody); err != nil {
				_ = child.Close()
				_ = current.Close()
				return "", err
			}
			info, err := child.Stat()
			if err != nil {
				_ = child.Close()
				_ = current.Close()
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				if seen[physical] || len(seen) >= 40 {
					_ = child.Close()
					_ = current.Close()
					return "", errors.New("runtime capture: cyclic or overlong kernel link chain")
				}
				seen[physical] = true
				target, err := runtimeReadlink(child)
				_ = child.Close()
				_ = current.Close()
				if err != nil {
					return "", err
				}
				facts := snapshot.Custody[physical]
				links = append(links, RuntimeLink{Path: physical, Target: target, Origin: origin, Observation: &facts})
				if filepath.IsAbs(target) {
					pending = target
				} else {
					pending = filepath.Join(filepath.Dir(physical), target)
				}
				if index+1 < len(parts) {
					pending = filepath.Join(pending, filepath.Join(parts[index+1:]...))
				}
				pending = filepath.Clean(pending)
				restarted = true
				break
			}
			_ = current.Close()
			current = child
			if index+1 < len(parts) && !info.IsDir() {
				_ = current.Close()
				return "", errors.New("runtime capture: source link crosses non-directory")
			}
		}
		if restarted {
			continue
		}
		info, err := current.Stat()
		_ = current.Close()
		if err != nil {
			return "", err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return "", errors.New("runtime capture: link target is not retained code or resources")
		}
		if physical != origin.SelectedRoot && !strings.HasPrefix(physical, origin.SelectedRoot+"/") {
			return "", errors.New("runtime capture: link target leaves explicit selected root")
		}
		for _, link := range links {
			link.ResolvedPath = physical
			link.CapturedPath = runtimeViewPath(physical)
			if previous, exists := snapshot.Links[link.Path]; exists && !reflect.DeepEqual(previous, link) {
				return "", errors.New("runtime capture: conflicting source link provenance")
			}
			snapshot.Links[link.Path] = link
		}
		return physical, nil
	}
}
