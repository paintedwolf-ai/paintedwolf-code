package confine

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
)

func isWindowsAbsPath(path string) bool {
	if len(path) < 2 {
		return false
	}
	return path[1] == ':'
}

func osDefaultWriteRoots() []string {
	return append(standardCacheDataRoots(), platformUserCache())
}

// WorkspaceRoots separates session storage from a single invocation’s private stage.
type WorkspaceRoots struct {
	SessionScratch string
	Action         ActionWorkspace `json:"-"`
}

// ActionWorkspace holds a private working directory for one tool invocation.
type ActionWorkspace struct{ path string }

func NewActionWorkspace() (ActionWorkspace, error) {
	root, err := configdir.UserConfigDir()
	if err != nil {
		return ActionWorkspace{}, err
	}
	parent := enginepaths.ActionWorkRootUnder(root)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return ActionWorkspace{}, err
	}
	path, err := os.MkdirTemp(parent, "action-")
	return ActionWorkspace{path: path}, err
}

func (w ActionWorkspace) Path() string { return w.path }

func (c Confinement) privateWorkRoots() []string {
	var paths []string
	for _, dir := range []string{c.Workspaces.SessionScratch, c.Workspaces.Action.Path()} {
		if dir != "" {
			paths = append(paths, fspath.CanonicalPath(dir))
		}
	}
	return paths
}

func (c Confinement) writeRoots() ([]string, error) {
	granted := c.GrantedWriteRoots
	if c.Workspaces.Action.Path() != "" {
		granted = append(slices.Clone(granted), c.Workspaces.Action.Path())
	}
	return WriteRootsForBoundary(c.ProjectID, c.Roots, granted, c.Workspaces.SessionScratch)
}

// WriteRootsForBoundary is the write profile for one confinement: attached
// roots, session scratch, OS caches, durable grants, and per-action granted overlays.
func WriteRootsForBoundary(projectID string, projectRoots, granted []string, sessionScratchRoot string) ([]string, error) {
	return filesystemRoots(projectID, projectRoots, granted, sessionScratchRoot, osDefaultWriteRoots())
}

func filesystemRoots(projectID string, projectRoots, granted []string, sessionScratchRoot string, defaults []string) ([]string, error) {
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
	for _, root := range defaults {
		if !defaultRootContainsStateFolder(fspath.CanonicalPath(root)) {
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

// PathWithinRoots matches existing and not-yet-created paths.
func PathWithinRoots(path string, roots []string) bool {
	p := fspath.CanonicalPath(path)
	if p == "" {
		return false
	}
	for _, root := range roots {
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

// NativeFilesystemRootForPath resolves external filesystem authority for native operations.
func NativeFilesystemRootForPath(projectID string, roots, granted []string, sessionScratch, path string, write bool) (string, error) {
	if !filepath.IsAbs(path) {
		return "", nil
	}
	abs := fspath.CanonicalPath(path)
	if abs == "" {
		return "", nil
	}
	var allowed []string
	var err error
	switch {
	case write:
		allowed, err = filesystemRoots(projectID, sharedTempScopedRoots(roots, abs), granted, sessionScratch, nil)
	case SharedTempLocation(abs) != "":
		allowed, err = explicitSharedTempReadRoots(roots, nil, nil, sessionScratch, abs)
	default:
		allowed, err = filesystemRoots(projectID, roots, granted, sessionScratch, osDefaultWriteRoots())
	}
	if err != nil {
		return "", err
	}
	return deepestCoveringRoot(allowed, abs), nil
}

// NativeReadRootForPath resolves external read authority, honoring exact and directory read grants.
func NativeReadRootForPath(projectID string, roots, readRoots, sessionReadPaths, grantedWriteRoots []string, sessionScratch, sessionOutput, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", nil
	}
	abs := fspath.CanonicalPath(path)
	if abs == "" {
		return "", nil
	}
	if ControlPlanePathDenied(abs, false, sessionScratch, sessionOutput) {
		return "", nil
	}
	effectiveReadRoots := slices.Clone(readRoots)
	if strings.TrimSpace(sessionOutput) != "" {
		effectiveReadRoots = append(effectiveReadRoots, sessionOutput)
	}
	if SharedTempLocation(abs) == "" {
		allowed, err := filesystemRoots(projectID, roots, grantedWriteRoots, sessionScratch, osDefaultWriteRoots())
		if err != nil {
			return "", err
		}
		for _, r := range effectiveReadRoots {
			if cp := fspath.CanonicalPath(r); cp != "" {
				allowed = append(allowed, cp)
			}
		}
		return deepestCoveringRoot(allowed, abs), nil
	}
	allowed, err := explicitSharedTempReadRoots(roots, effectiveReadRoots, sessionReadPaths, sessionScratch, abs)
	if err != nil {
		return "", err
	}
	return deepestCoveringRoot(allowed, abs), nil
}

// DefaultReadRoots lists ambient non-temp roots admitted for native reads.
func DefaultReadRoots(projectID string) []string {
	var out []string
	for _, raw := range append(osDefaultWriteRoots(), grantedWriteRoots(projectID)...) {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		cp := fspath.CanonicalPath(raw)
		if cp == "" || SharedTempLocation(cp) != "" || defaultRootContainsStateFolder(cp) {
			continue
		}
		if !slices.Contains(out, cp) {
			out = append(out, cp)
		}
	}
	return out
}

func sharedTempScopedRoots(roots []string, abs string) []string {
	loc := SharedTempLocation(abs)
	if loc == "" {
		return roots
	}
	var out []string
	for _, r := range roots {
		if cp := fspath.CanonicalPath(r); cp != "" && PathAtOrUnder(cp, loc) {
			out = append(out, r)
		}
	}
	return out
}

func explicitSharedTempReadRoots(roots, readRoots, sessionReadPaths []string, sessionScratch, abs string) ([]string, error) {
	loc := SharedTempLocation(abs)
	var allowed []string
	for _, r := range append(slices.Clone(roots), readRoots...) {
		if cp := fspath.CanonicalPath(r); cp != "" && PathAtOrUnder(cp, loc) {
			allowed = append(allowed, cp)
		}
	}
	if sessionScratch != "" {
		if cp := fspath.CanonicalPath(sessionScratch); cp != "" {
			allowed = append(allowed, cp)
		}
	}
	rawGrants := make([]ProtectedPathGrant, 0, len(sessionReadPaths))
	for _, p := range sessionReadPaths {
		if strings.TrimSpace(p) != "" {
			rawGrants = append(rawGrants, NewProtectedPathGrant(p))
		}
	}
	applied, _, err := validateProtectedPathGrants(normalizeProtectedPathGrants(rawGrants))
	if err != nil {
		return nil, err
	}
	for _, g := range applied {
		if g.Subtree {
			if PathAtOrUnder(g.ResolvedPath, loc) || PathAtOrUnder(loc, g.ResolvedPath) {
				allowed = append(allowed, g.ResolvedPath)
			}
			continue
		}
		if PathEqual(abs, g.ResolvedPath) {
			allowed = append(allowed, g.ResolvedPath)
		}
	}
	return allowed, nil
}

func deepestCoveringRoot(allowed []string, abs string) string {
	var best string
	for _, root := range allowed {
		rel, err := filepath.Rel(root, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && len(root) > len(best) {
			best = root
		}
	}
	return best
}

// UnrecordedRoots are the host folders whose changes source history never
// records outside a project: temporary folders, caches, tool data and the
// host's own state.
func UnrecordedRoots() []string {
	roots := controlPlaneReadDenyRootsResolved()
	for _, root := range append(osDefaultWriteRoots(), SharedTempLocations()...) {
		if canonical := fspath.CanonicalPath(root); canonical != "" {
			roots = append(roots, canonical)
		}
	}
	return roots
}
