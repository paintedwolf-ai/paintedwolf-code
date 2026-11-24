package confine

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/configlayout"
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

// ReadableSharedTempLocations lists shared locations covered by live read directory grants.
func ReadableSharedTempLocations(grants []string) []string {
	var allowed []string
	for _, path := range grants {
		g := NewProtectedPathGrant(path)
		if !g.Subtree {
			continue
		}
		for _, location := range SharedTempLocations() {
			if PathAtOrUnder(location, g.ResolvedPath) && !slices.Contains(allowed, location) {
				allowed = append(allowed, location)
			}
		}
	}
	return allowed
}

func sharedTempWriteGrant(path string) string {
	clean := fspath.CanonicalPath(path)
	if clean == "" {
		clean = filepath.Clean(path)
	}
	loc := SharedTempLocation(clean)
	if loc == "" {
		return filepath.Dir(clean)
	}
	if PathEqual(clean, loc) {
		return loc
	}
	dir := filepath.Dir(clean)
	if !PathAtOrUnder(dir, loc) {
		return loc
	}
	return dir
}

func attachedTempProjectRoots(c Confinement, sharedTemp []string) []string {
	var out []string
	for _, root := range resolveEach(c.Roots) {
		if underAny(root, sharedTemp) {
			out = append(out, root)
		}
	}
	return out
}

// addSharedTempReadFloor denies ambient reads in shared temp, restores explicit
// read scopes, and keeps metadata traversal on write-only temp directories.
func (r *FilesystemRules) addSharedTempReadFloor(c Confinement) []string {
	if len(r.sharedTemp) == 0 {
		return nil
	}
	r.add(fsRule{op: opRead, matches: subpathMatches(r.sharedTemp, FloorSharedTemp)})
	r.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(withDarwinAliases(r.sharedTemp), "")})
	excluded := resolveEach(c.ReadDenyPaths)
	var readSubpaths, readLiterals, reachable []string
	if runtime.GOOS == "darwin" {
		if ut := fspath.CanonicalPath(platformUserTemp()); ut != "" {
			xcrun := filepath.Join(ut, "xcrun_db")
			if !underAny(xcrun, excluded) {
				readLiterals = append(readLiterals, xcrun)
				reachable = append(reachable, xcrun)
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		if resolved := fspath.CanonicalPath(exe); resolved != "" && underAny(resolved, r.sharedTemp) && !underAny(resolved, excluded) {
			readLiterals = append(readLiterals, resolved)
			reachable = append(reachable, resolved)
		}
	}
	if pinned := strings.TrimSpace(c.PinnedExecutable); pinned != "" {
		if resolved := fspath.CanonicalPath(pinned); resolved != "" && underAny(resolved, r.sharedTemp) && !underAny(resolved, excluded) {
			readLiterals = append(readLiterals, resolved)
			reachable = append(reachable, resolved)
		}
	}
	if engineRoot := fspath.CanonicalPath(configlayout.EngineRoot()); engineRoot != "" {
		gitRoot := filepath.Join(engineRoot, "gitengine")
		if info, err := os.Stat(gitRoot); err == nil && info.IsDir() && strictlyUnderAny(gitRoot, r.sharedTemp) && !underAny(gitRoot, excluded) {
			readSubpaths = append(readSubpaths, gitRoot)
			reachable = append(reachable, gitRoot)
		}
	}
	for _, root := range append(nonExcludedPaths(resolveEach(c.Roots), excluded), nonExcludedPaths(resolveEach(c.ReadRoots), excluded)...) {
		if underAny(root, r.sharedTemp) {
			readSubpaths = append(readSubpaths, root)
			reachable = append(reachable, root)
		}
	}
	for _, root := range append(c.privateWorkRoots(), workspaceReadAllowBacks(c)...) {
		if underAny(root, r.sharedTemp) {
			readSubpaths = append(readSubpaths, root)
			reachable = append(reachable, root)
		}
		for _, loc := range r.sharedTemp {
			if PathStrictlyUnder(loc, root) {
				readSubpaths = append(readSubpaths, loc)
				reachable = append(reachable, loc)
			}
		}
	}
	for _, g := range c.ProtectedReadGrants {
		if !underAny(g.ResolvedPath, r.sharedTemp) && !coversAnyTempLocation(g, r.sharedTemp) {
			continue
		}
		reachable = append(reachable, g.ResolvedPath)
		if g.Subtree {
			readSubpaths = append(readSubpaths, g.ResolvedPath)
		} else {
			readLiterals = append(readLiterals, g.ResolvedPath)
		}
	}
	r.add(fsRule{
		op: opRead, allow: true,
		matches: append(subpathMatches(withDarwinAliases(readSubpaths), ""), literalMatches(withDarwinAliases(readLiterals), "")...),
	})
	writeOnly := r.writeOnlySharedTempRoots(c, readSubpaths)
	r.add(fsRule{op: opReadMetadata, allow: true, matches: subpathMatches(withDarwinAliases(writeOnly), "")})
	reachable = append(reachable, writeOnly...)
	var socketLiterals []string
	for _, sock := range c.socketMetadataLiterals() {
		if underAny(sock, r.sharedTemp) {
			socketLiterals = append(socketLiterals, sock)
			reachable = append(reachable, sock)
		}
	}
	r.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(withDarwinAliases(socketLiterals), "")})
	for _, g := range append(slices.Clone(c.ProtectedWriteGrants), c.PolicyWriteGrants...) {
		if underAny(g.ResolvedPath, r.sharedTemp) {
			reachable = append(reachable, g.ResolvedPath)
		}
	}
	return reachable
}

func (c Confinement) socketMetadataLiterals() []string {
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		if clean := filepath.Clean(p); filepath.IsAbs(clean) && !slices.Contains(out, clean) {
			out = append(out, clean)
		}
		if canon := fspath.CanonicalPath(p); canon != "" && filepath.IsAbs(canon) && !slices.Contains(out, canon) {
			out = append(out, canon)
		}
	}
	for _, ep := range c.socketEndpoints {
		add(ep)
	}
	for _, hp := range c.HostServicePaths {
		add(hp)
	}
	if len(c.socketEndpoints) == 0 {
		for _, g := range c.SocketGrants {
			add(g.ApprovedPath)
			add(g.ResolvedPath)
		}
	}
	slices.Sort(out)
	return out
}

func (r *FilesystemRules) writeOnlySharedTempRoots(c Confinement, readSubpaths []string) []string {
	candidates := resolveEach(slices.Concat(c.GrantedWriteRoots, grantedWriteRoots(c.ProjectID), environmentWriteRoots()))
	var out []string
	for _, candidate := range candidates {
		if underAny(candidate, readSubpaths) {
			continue
		}
		if underAny(candidate, r.sharedTemp) {
			out = append(out, candidate)
			continue
		}
		for _, loc := range r.sharedTemp {
			if PathAtOrUnder(loc, candidate) && !underAny(loc, readSubpaths) {
				out = append(out, loc)
			}
		}
	}
	return out
}

func coversAnyTempLocation(g ProtectedPathGrant, sharedTemp []string) bool {
	if !g.Subtree {
		return false
	}
	for _, loc := range sharedTemp {
		if PathAtOrUnder(loc, g.ResolvedPath) {
			return true
		}
	}
	return false
}

func nonExcludedPaths(paths, excluded []string) []string {
	var out []string
	for _, path := range paths {
		if !slices.Contains(excluded, path) {
			out = append(out, path)
		}
	}
	return out
}

func withDarwinAliases(paths []string) []string {
	if runtime.GOOS != "darwin" || len(paths) == 0 {
		return paths
	}
	seen := make(map[string]bool, len(paths)*2)
	out := make([]string, 0, len(paths)*2)
	add := func(p string) {
		p = filepath.Clean(p)
		if p != "" && p != "." && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range paths {
		add(p)
		for _, alias := range []string{"/var", "/tmp"} {
			if rest, ok := pathUnder(p, "/private"+alias); ok {
				add(alias + rest)
			}
		}
	}
	return out
}

// A cache grant can overlap shared temp in either direction.
func (r *FilesystemRules) closeSharedTempInCaches(c Confinement, roots []string) {
	explicitGrants := resolveEach(slices.Concat(c.GrantedWriteRoots, grantedWriteRoots(c.ProjectID), environmentWriteRoots(), c.privateWorkRoots()))
	attachedRoots := resolveEach(c.Roots)
	for _, location := range r.sharedTemp {
		if underAny(location, explicitGrants) {
			continue
		}
		var explicit []string
		for _, root := range slices.Concat(attachedRoots, explicitGrants) {
			if PathAtOrUnder(root, location) {
				explicit = append(explicit, root)
			}
		}
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
