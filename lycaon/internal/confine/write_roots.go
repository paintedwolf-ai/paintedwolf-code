package confine

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// PathWithinWriteRoots matches existing and not-yet-created paths.
func PathWithinWriteRoots(path string, writeRoots []string) bool {
	p := fspath.CanonicalPath(path)
	if p == "" {
		return false
	}
	for _, root := range writeRoots {
		r := fspath.CanonicalPath(root)
		if r == "" {
			continue
		}
		if PathAtOrUnder(p, r) {
			return true
		}
	}
	return false
}

func isWindowsAbsPath(path string) bool {
	if len(path) < 2 {
		return false
	}
	return path[1] == ':'
}

// WriteRootsForBoundary is the write profile for one confinement: attached
// roots, session scratch, OS caches, durable grants, and per-action granted overlays.
func WriteRootsForBoundary(projectID string, projectRoots, granted []string, sessionScratchRoot string) ([]string, error) {
	roots := []string{}
	add := func(p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		cp := fspath.CanonicalPath(p)
		if cp != "" {
			roots = append(roots, cp)
		}
		// Seatbelt checks paths before symlink traversal; register both raw and canonical Darwin aliases.
		if runtime.GOOS == "darwin" {
			for _, alias := range []string{"/var", "/tmp"} {
				if rest, ok := pathUnder(cp, "/private"+alias); ok {
					roots = append(roots, alias+rest)
				}
				if _, ok := pathUnder(filepath.Clean(p), alias); ok {
					roots = append(roots, filepath.Clean(p))
				}
			}
		}
	}
	for _, r := range projectRoots {
		add(r)
	}
	if sessionScratchRoot != "" {
		add(sessionScratchRoot)
	}
	standard := append([]string{"/tmp", "/private/tmp", "/var/tmp", "/private/var/tmp", os.TempDir()}, standardCacheDataRoots()...)
	standard = append(standard, darwinUserRoots()...)
	for _, root := range standard {
		if !writeRootContainsStateTree(fspath.CanonicalPath(root)) {
			add(root)
		}
	}
	for _, r := range granted {
		add(r)
	}
	// Durable grants are resolved for each execution.
	for _, r := range grantedWriteRoots(projectID) {
		add(r)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, r := range roots {
		r = filepath.Clean(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	if err := validateEffectiveWriteRoots(out); err != nil {
		return nil, err
	}
	return out, nil
}
