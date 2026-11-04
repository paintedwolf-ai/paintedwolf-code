//go:build linux

package packagegeneration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ResolveSystemExecutable selects the physical executable behind administrator
// controlled aliases. Both aliases and targets must stay under trusted ancestry.
func ResolveSystemExecutable(path string) (string, error) {
	return native.systemExecutable(path)
}

func (l layout) systemExecutable(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("native generation: unclean system executable %q", path)
	}
	relative, err := filepath.Rel(l.anchor, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", fmt.Errorf("native generation: executable outside trust anchor: %s", path)
	}
	if err := l.controlledParents(l.anchor); err != nil {
		return "", err
	}
	pending, current, links := strings.Split(relative, "/"), l.anchor, 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		if component == "." || component == "" {
			continue
		}
		if component == ".." {
			if current == l.anchor {
				return "", fmt.Errorf("native generation: executable alias escapes trust anchor")
			}
			current = filepath.Dir(current)
			continue
		}
		candidate := filepath.Join(current, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", fmt.Errorf("system executable %s: %w", candidate, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 && stat.Uid != l.owner {
			return "", fmt.Errorf("native generation: uncontrolled system executable %s", candidate)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			links++
			if links > 40 {
				return "", fmt.Errorf("system executable %s: %w", candidate, syscall.ELOOP)
			}
			target, err := os.Readlink(candidate)
			if err != nil {
				return "", err
			}
			if filepath.IsAbs(target) {
				target, err = filepath.Rel(l.anchor, target)
				if err != nil || target == ".." || strings.HasPrefix(target, "../") {
					return "", fmt.Errorf("native generation: absolute executable alias escapes trust anchor")
				}
				current = l.anchor
			}
			pending = append(strings.Split(target, "/"), pending...)
			continue
		}
		if info.Mode().Perm()&0o022 != 0 || len(pending) > 0 && !info.IsDir() {
			return "", fmt.Errorf("native generation: uncontrolled system executable ancestor %s", candidate)
		}
		if len(pending) == 0 {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
				return "", fmt.Errorf("native generation: system executable is not an executable file: %s", candidate)
			}
			return candidate, nil
		}
		current = candidate
	}
	return "", fmt.Errorf("native generation: system executable names the trust anchor")
}
