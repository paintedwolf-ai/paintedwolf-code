package confine

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// WriteRootsForProject returns the compiled write-root union.
func WriteRootsForProject(projectID string, projectRoots []string) []string {
	return WriteRootsForBoundary(projectID, projectRoots, nil, "")
}

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
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

func resolveArgPath(arg, projectDir string) (string, bool) {
	p := arg
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		if arg == "~" {
			p = home
		} else {
			p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	abs := strings.HasPrefix(p, "/") || isWindowsAbsPath(p)
	if !abs {
		if projectDir == "" {
			return "", false
		}
		p = filepath.Join(projectDir, p)
	}
	p = fspath.CanonicalPath(p)
	return p, p != ""
}

func isWindowsAbsPath(path string) bool {
	if len(path) < 2 {
		return false
	}
	return path[1] == ':'
}

// WriteRootsForBoundary is the write profile for one confinement: attached
// roots, session scratch, OS caches, durable grants, and per-action granted overlays.
func WriteRootsForBoundary(projectID string, projectRoots, granted []string, sessionScratchRoot string) []string {
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
	add("/tmp")
	add("/private/tmp")
	add("/var/tmp")
	add("/private/var/tmp")
	add(os.TempDir())
	for _, r := range standardCacheDataRoots() {
		add(r)
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
		r = strings.TrimRight(r, "/")
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func validatedWriteRoots(projectID string, projectRoots, granted []string, sessionScratchRoot string) ([]string, error) {
	roots := WriteRootsForBoundary(projectID, projectRoots, granted, sessionScratchRoot)
	grantedSet := map[string]bool{}
	for _, g := range append(append([]string(nil), granted...), grantedWriteRoots(projectID)...) {
		if g = strings.TrimSpace(g); g == "" {
			continue
		}
		grantedSet[strings.TrimRight(fspath.CanonicalPath(g), "/")] = true
	}
	if err := validateEffectiveWriteRoots(roots, grantedSet); err != nil {
		return nil, err
	}
	return roots, nil
}
