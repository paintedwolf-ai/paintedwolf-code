package confine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// packageCacheHomeRelRoots are home-relative package stores allowed as default
// write roots. Each names the store, not the tool home, because install and bin
// directories execute outside the sandbox.
var packageCacheHomeRelRoots = []string{
	".bun/install/cache",
	".npm/_cacache",
	".cargo/registry",
	".cargo/git",
}

// toolchainStateRoots are state directories a toolchain writes on every run
// and tolerates losing. Go's local telemetry counters live under the user
// config directory, outside every cache convention; without this root each
// confined go invocation reports a refusal.
func toolchainStateRoots() []string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return nil
	}
	return []string{filepath.Join(dir, "go", "telemetry")}
}

// standardCacheDataRoots composes platform conventions and shared write authority.
func standardCacheDataRoots() []string {
	home, _ := os.UserHomeDir()
	candidates := platformCacheDataRoots(home)
	for _, rel := range packageCacheHomeRelRoots {
		candidates = append(candidates, packageCacheHomePath(home, rel))
	}
	candidates = append(candidates, toolchainStateRoots()...)
	candidates = append(candidates, environmentWriteRoots()...)
	roots := []string{}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) != "" {
			roots = append(roots, candidate)
		}
	}
	return roots
}

func packageCacheHomePath(home, relative string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, relative)
}

// SharedTempLocations returns the canonical machine temp directories.
func SharedTempLocations() []string {
	var locations []string
	for _, path := range []string{"/tmp", "/var/tmp", os.TempDir(), platformUserTemp()} {
		if !filepath.IsAbs(path) {
			continue
		}
		location := fspath.CanonicalPath(path)
		if location != "" && filepath.Dir(location) != location && !slices.Contains(locations, location) {
			locations = append(locations, location)
		}
	}
	return locations
}

// SharedTempLocation selects the narrowest location covering path.
func SharedTempLocation(path string) string {
	if !filepath.IsAbs(path) {
		return ""
	}
	path = fspath.CanonicalPath(path)
	var best string
	for _, location := range SharedTempLocations() {
		if len(location) > len(best) && PathAtOrUnder(path, location) {
			best = location
		}
	}
	return best
}

// WritableSharedTempLocations lists the shared locations covered by live write roots.
func WritableSharedTempLocations(projectID string, grants []string) []string {
	roots := append(slices.Clone(grants), grantedWriteRoots(projectID)...)
	var allowed []string
	for _, location := range SharedTempLocations() {
		if PathWithinRoots(location, roots) {
			allowed = append(allowed, location)
		}
	}
	return allowed
}

// A cache grant can overlap shared temp in either direction.
func (r *FilesystemRules) closeSharedTempInCaches(c Confinement, roots []string) {
	explicit := resolveEach(slices.Concat(c.Roots, c.GrantedWriteRoots, grantedWriteRoots(c.ProjectID), environmentWriteRoots(), []string{c.SessionScratchRoot}))
	for _, location := range r.sharedTemp {
		if underAny(location, explicit) {
			continue
		}
		overlaps := slices.ContainsFunc(roots, func(root string) bool {
			return !underAny(root, explicit) && (PathAtOrUnder(root, location) || PathAtOrUnder(location, root))
		})
		if !overlaps {
			continue
		}
		var except []string
		for _, root := range explicit {
			if PathStrictlyUnder(root, location) {
				except = append(except, root)
			}
		}
		r.add(fsRule{op: opWrite, matches: []fsMatch{{subpath: location, except: except, layer: FloorSharedTemp}}})
	}
}
