package confine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
)

// addControlPlaneReadFloor keeps directory read grants from exposing control
// data nested within the approved path. The invocation's own session scratch
// stays readable; other sessions' scratch remains behind the floor.
func addControlPlaneReadFloor(rules *FilesystemRules, c Confinement) {
	if secretReadDenyDisabled() {
		return
	}
	denied := resolveEach(controlPlaneReadDenyRoots())
	if len(denied) == 0 {
		return
	}
	rules.add(fsRule{op: opRead, matches: subpathMatches(denied, FloorReadControlPlane)})
	allowed := append(workspaceReadAllowBacks(c), resolveEach(c.ReadRoots)...)
	if c.SessionScratchRoot != "" {
		allowed = append(allowed, fspath.CanonicalPath(c.SessionScratchRoot))
	}
	addManagedReadRoots(rules, subpathMatches(denied, FloorReadControlPlane), allowed)
	var matches []fsMatch
	reachable := append([]string(nil), allowed...)
	for _, g := range excludedWorkspaceGrants(c) {
		reachable = append(reachable, g.ResolvedPath)
		if g.Subtree {
			matches = append(matches, fsMatch{subpath: g.ResolvedPath})
		} else {
			matches = append(matches, fsMatch{literal: g.ResolvedPath})
		}
	}
	rules.add(fsRule{op: opRead, allow: true, matches: matches})
	if len(reachable) == 0 {
		return
	}
	rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(traversalAncestors(denied, reachable), "")})
}

// addManagedReadRoots reopens managed read roots beneath a read denial. They
// override denied ancestors only: a denial below a managed root still holds,
// and only an approved read grant lifts that.
func addManagedReadRoots(rules *FilesystemRules, denied []fsMatch, managed []string) {
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches(managed, "")})
	var below []fsMatch
	var belowPaths []string
	for _, m := range denied {
		path := m.literal
		if path == "" {
			path = m.subpath
		}
		if path != "" && strictlyUnderAny(path, managed) {
			below = append(below, m)
			belowPaths = append(belowPaths, path)
		}
	}
	if len(below) == 0 {
		return
	}
	rules.add(fsRule{op: opRead, matches: below})
	var inner []string
	for _, root := range managed {
		if strictlyUnderAny(root, belowPaths) {
			inner = append(inner, root)
		}
	}
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches(inner, "")})
}

func strictlyUnderAny(path string, ancestors []string) bool {
	for _, ancestor := range ancestors {
		if path != ancestor && PathAtOrUnder(path, ancestor) {
			return true
		}
	}
	return false
}

// workspaceReadAllowBacks lists the managed workspaces an invocation may read.
// A root the invocation excludes loses its carve-out, so its own read roots
// are all that remains readable inside.
func workspaceReadAllowBacks(c Confinement) []string {
	excluded := resolveEach(c.ReadDenyPaths)
	var out []string
	for _, root := range SecretReadAllowBackRoots() {
		if !underAny(root, excluded) {
			out = append(out, root)
		}
	}
	return out
}

// excludedWorkspaceGrants lists approved reads inside an excluded workspace. The
// control-plane floor would otherwise take them back; everywhere else inside the
// state tree it still wins over a grant.
func excludedWorkspaceGrants(c Confinement) []ProtectedPathGrant {
	excluded := resolveEach(c.ReadDenyPaths)
	var out []ProtectedPathGrant
	for _, g := range c.ProtectedReadGrants {
		if underSecretReadAllowBack(g.ResolvedPath) && underAny(g.ResolvedPath, excluded) {
			out = append(out, g)
		}
	}
	return out
}

// WorkerReadDenyRoots lists the managed workspaces a worker never reads: every
// worker branch, its own restored by name, and for a write leg the session
// worktrees and drafts its sources live in, since it works from its private copy.
func WorkerReadDenyRoots(writeLeg bool) []string {
	dir, err := configdir.Root()
	if err != nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	out := []string{enginepaths.WorkerBranchesRootUnder(dir)}
	if writeLeg {
		out = append(out, enginepaths.SessionWorktreesRootUnder(dir), enginepaths.DraftsRootUnder(dir))
	}
	return out
}

// secretReadDenyDisabled reports an explicit read-floor opt-out.
func secretReadDenyDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LYCAON_SANDBOX_DENY_READ"))) {
	case "off", "none", "disable", "disabled":
		return true
	}
	return false
}

// requireReadFloorResolvable checks inputs before emitting the read floor.
func requireReadFloorResolvable() error {
	if !secretReadDenyDisabled() {
		if _, err := configdir.UserConfigDir(); err != nil {
			return fmt.Errorf("confine: control-plane read floor unresolvable: %w", err)
		}
	}
	// Key-material inputs remain required after a control-plane opt-out.
	if !homeRelPathsResolvable(catalogedKeyMaterialPaths()) {
		return fmt.Errorf("confine: key-material read floor unresolvable: home directory unreadable")
	}
	return nil
}

// homeRelPathsResolvable reports whether every ~/-relative entry can be
// joined onto a home directory.
func homeRelPathsResolvable(paths []string) bool {
	needsHome := false
	for _, p := range paths {
		if strings.HasPrefix(strings.TrimSpace(p), "~/") {
			needsHome = true
			break
		}
	}
	if !needsHome {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && strings.TrimSpace(home) != ""
}

func SecretReadDenyRoots() []string {
	if secretReadDenyDisabled() {
		return nil
	}
	env := strings.TrimSpace(os.Getenv("LYCAON_SANDBOX_DENY_READ"))
	raw := controlPlaneReadDenyRoots()
	for _, e := range filepath.SplitList(env) {
		if strings.TrimSpace(e) != "" {
			raw = append(raw, e)
		}
	}
	return resolveEach(raw)
}

// traversalAncestors returns denied parents needed for path traversal.
func traversalAncestors(deny, allow []string) []string {
	sep := string(filepath.Separator)
	inDeny := func(p string) bool {
		for _, d := range deny {
			d = filepath.Clean(d)
			if p == d || strings.HasPrefix(p, d+sep) {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	var out []string
	for _, a := range allow {
		for dir := filepath.Dir(filepath.Clean(a)); inDeny(dir); dir = filepath.Dir(dir) {
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
			if parent := filepath.Dir(dir); parent == dir {
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// SecretReadAllowBackRoots returns readable workspace carve-outs.
func SecretReadAllowBackRoots() []string {
	if SecretReadDenyRoots() == nil {
		return nil
	}
	return resolveEach(agentWorkspaceRootsUnderControlPlane())
}

// agentWorkspaceRootsUnderControlPlane returns the engine-managed workspaces
// inside the state tree, independent of the read-floor switch.
func agentWorkspaceRootsUnderControlPlane() []string {
	dir, err := configdir.Root()
	if err != nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	return enginepaths.AgentWorkspaceRootsUnder(dir)
}

// controlPlaneReadDenyRoots returns the host-managed configuration tree.
func controlPlaneReadDenyRoots() []string {
	dir, err := configdir.Root()
	if err != nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	return []string{dir}
}

// resolveEach canonicalizes missing paths through their existing ancestor.
func resolveEach(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if rp := fspath.CanonicalPath(p); rp != "" {
			out = append(out, rp)
		} else {
			out = append(out, p)
		}
	}
	return out
}
