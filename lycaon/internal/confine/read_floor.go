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
	"github.com/lycaon/lycaon/internal/workerplacement"
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
	addManagedReadRoots(rules, append(subpathMatches(denied, FloorReadControlPlane), readDenyMatches(c)...), allowed)
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
	deniedPaths := make([]string, 0, len(denied))
	for _, m := range denied {
		if path := matchPath(m); path != "" {
			deniedPaths = append(deniedPaths, path)
		}
	}
	var reopened []string
	for _, root := range managed {
		if underAny(root, deniedPaths) {
			reopened = append(reopened, root)
		}
	}
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches(reopened, "")})
	var below []fsMatch
	var belowPaths []string
	for _, m := range denied {
		if path := matchPath(m); path != "" && strictlyUnderAny(path, reopened) {
			below = append(below, m)
			belowPaths = append(belowPaths, path)
		}
	}
	rules.add(fsRule{op: opRead, matches: below})
	var inner []string
	for _, root := range reopened {
		if strictlyUnderAny(root, belowPaths) {
			inner = append(inner, root)
		}
	}
	rules.add(fsRule{op: opRead, allow: true, matches: subpathMatches(inner, "")})
}

func matchPath(m fsMatch) string {
	if m.literal != "" {
		return m.literal
	}
	return m.subpath
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

// configuredReadDenyRoots returns the explicit extra read-deny roots from
// LYCAON_SANDBOX_DENY_READ when the read floor is enabled.
func configuredReadDenyRoots() []string {
	if secretReadDenyDisabled() {
		return nil
	}
	env := strings.TrimSpace(os.Getenv("LYCAON_SANDBOX_DENY_READ"))
	home, _ := os.UserHomeDir()
	home = strings.TrimSpace(home)
	var raw []string
	for _, e := range filepath.SplitList(env) {
		trimmed := strings.TrimSpace(e)
		if trimmed == "" {
			continue
		}
		if (strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`)) && home != "" {
			trimmed = filepath.Join(home, trimmed[2:])
		}
		raw = append(raw, trimmed)
	}
	return resolveEach(raw)
}

func SecretReadDenyRoots() []string {
	if secretReadDenyDisabled() {
		return nil
	}
	return append(resolveEach(controlPlaneReadDenyRoots()), configuredReadDenyRoots()...)
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
	if secretReadDenyDisabled() {
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
	roots := enginepaths.AgentWorkspaceRootsUnder(dir)
	for _, folder := range placementFolders(dir) {
		roots = append(roots, folder.BranchesRoot())
	}
	return roots
}

// placementFolders are reachable worker branch folders on other volumes. A folder
// on an absent volume has nothing to protect, and its path cannot be resolved.
func placementFolders(stateRoot string) []workerplacement.Folder {
	folders, err := workerplacement.AvailableFolders(stateRoot)
	if err != nil {
		return nil
	}
	return folders
}

// controlPlaneReadDenyRoots returns the host-managed configuration tree.
func controlPlaneReadDenyRoots() []string {
	dir, err := configdir.Root()
	if err != nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	roots := []string{dir}
	for _, folder := range placementFolders(dir) {
		roots = append(roots, folder.Path)
	}
	return roots
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

// AgentReadFloor is a precompiled per-root authorization projection that
// filters out control-plane state, configured extra read-deny roots, and
// catalogued key-material paths for AgentAudience views.
type AgentReadFloor struct {
	rootClean              string
	rootCanon              string
	effScratch             string
	effOutput              string
	whollyDenied           bool
	deniedRelPrefixes      []string
	unconditionalDenyRoots []string
	inPlacementBranch      bool
}

func parseSessionDirs(sessionDirs []string) (scratch, output string) {
	if len(sessionDirs) > 0 {
		scratch = strings.TrimSpace(sessionDirs[0])
	}
	if len(sessionDirs) > 1 {
		output = strings.TrimSpace(sessionDirs[1])
	}
	return scratch, output
}

func unconditionalAgentDenyRoots() []string {
	configured := configuredReadDenyRoots()
	keyMaterial := KeyMaterialReadDenyPaths()
	if len(configured) == 0 {
		return keyMaterial
	}
	if len(keyMaterial) == 0 {
		return configured
	}
	out := make([]string, 0, len(configured)+len(keyMaterial))
	out = append(out, configured...)
	out = append(out, keyMaterial...)
	return out
}

// CompileAgentReadFloor compiles the authoritative agent read floor for
// rootPath, optionally scoped with the invocation's own sessionScratch and
// sessionOutput directories.
func CompileAgentReadFloor(rootPath string, sessionDirs ...string) AgentReadFloor {
	clean := filepath.Clean(strings.TrimSpace(rootPath))
	if clean == "" || clean == "." {
		return AgentReadFloor{}
	}
	canon := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(clean)))
	if canon == "" || canon == "." {
		canon = clean
	}
	rawScratch, rawOutput := parseSessionDirs(sessionDirs)
	effScratch, effOutput := EffectiveSessionDirsForRoot(clean, rawScratch, rawOutput)
	unconditional := unconditionalAgentDenyRoots()
	floor := AgentReadFloor{
		rootClean:              clean,
		rootCanon:              canon,
		effScratch:             effScratch,
		effOutput:              effOutput,
		unconditionalDenyRoots: unconditional,
	}

	seen := make(map[string]struct{})
	addDeniedRel := func(rel string) {
		if rel == "" || rel == "." {
			return
		}
		lower := strings.ToLower(rel)
		if _, exists := seen[lower]; !exists {
			seen[lower] = struct{}{}
			floor.deniedRelPrefixes = append(floor.deniedRelPrefixes, lower)
		}
	}

	// 1. Unconditional extra read-deny roots (LYCAON_SANDBOX_DENY_READ) and
	// pack-derived key-material roots apply without any managed-workspace or
	// session carve-out.
	for _, extra := range unconditional {
		for _, denyVariant := range pathVariants(extra) {
			if _, underDeny := enginepaths.RelUnderFold(denyVariant, clean); underDeny {
				floor.whollyDenied = true
				return floor
			}
			if _, underDeny := enginepaths.RelUnderFold(denyVariant, canon); underDeny {
				floor.whollyDenied = true
				return floor
			}
			for _, base := range []string{clean, canon} {
				if rel, ok := enginepaths.RelUnderFold(base, denyVariant); ok && rel != "." {
					addDeniedRel(rel)
				}
			}
		}
	}

	// 2. Worker placement folders outside an explicitly attached branch root.
	if enginepaths.IsPlacementExcludedLexical(clean, clean) || enginepaths.IsPlacementExcludedLexical(canon, canon) {
		floor.whollyDenied = true
		return floor
	}
	if _, _, ok := enginepaths.SplitWorkerPlacementPath(clean); ok {
		floor.inPlacementBranch = true
	} else if _, _, ok := enginepaths.SplitWorkerPlacementPath(canon); ok {
		floor.inPlacementBranch = true
	}

	inOwnedSession := (effScratch != "" && WithinSessionDirectory(clean, effScratch)) ||
		(effOutput != "" && WithinSessionDirectory(clean, effOutput))
	if inOwnedSession {
		return floor
	}

	if enginepaths.IsObservedStateExcluded(clean, clean) {
		floor.whollyDenied = true
		return floor
	}

	// 3. Control-plane read floor: carves out only explicit managed workspaces
	// (and invocation-owned session directories handled above).
	if !secretReadDenyDisabled() {
		if cfgClean, cfgCanon, ok := enginepaths.CurrentConfigRoot(true); ok {
			if isRootDeniedByConfigTree(cfgClean, clean) || isRootDeniedByConfigTree(cfgCanon, canon) {
				floor.whollyDenied = true
				return floor
			}
			controlRoots := []string{cfgClean}
			if cfgCanon != cfgClean {
				controlRoots = append(controlRoots, cfgCanon)
			}
			for _, folder := range placementFolders(cfgClean) {
				controlRoots = append(controlRoots, pathVariants(folder.Path)...)
			}
			for _, ctrl := range controlRoots {
				for _, base := range []string{clean, canon} {
					if rel, ok := enginepaths.RelUnderFold(base, ctrl); ok && rel != "." {
						addDeniedRel(rel)
					}
				}
			}
		}
	}
	return floor
}

// WhollyDenied reports whether the entire root is denied to AgentAudience.
func (f AgentReadFloor) WhollyDenied() bool {
	return f.whollyDenied
}

// DeniedRelPrefixes returns the lowercased slash-separated relative path
// prefixes under rootPath that are denied to AgentAudience.
func (f AgentReadFloor) DeniedRelPrefixes() []string {
	return f.deniedRelPrefixes
}

// CacheKey returns a deterministic key component representing the floor's
// effective restrictions.
func (f AgentReadFloor) CacheKey() string {
	if !f.whollyDenied && len(f.deniedRelPrefixes) == 0 && f.effScratch == "" && f.effOutput == "" {
		return ""
	}
	prefixes := append([]string(nil), f.deniedRelPrefixes...)
	sort.Strings(prefixes)
	return fmt.Sprintf("%t:%s:%s:%s", f.whollyDenied, f.effScratch, f.effOutput, strings.Join(prefixes, "\x00"))
}

// IsDeniedRel reports whether relSlash under the compiled root is denied to
// AgentAudience.
func (f AgentReadFloor) IsDeniedRel(relSlash string) bool {
	if f.whollyDenied {
		return true
	}
	clean := strings.Trim(strings.TrimSpace(strings.ReplaceAll(relSlash, "\\", "/")), "/")
	clean = strings.TrimPrefix(clean, "./")
	if clean == "" || clean == "." {
		return false
	}
	if !f.inPlacementBranch && enginepaths.HasWorkerPlacementSegment(clean) {
		return true
	}
	lower := strings.ToLower(clean)
	for _, prefix := range f.deniedRelPrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+"/") {
			return true
		}
	}
	return false
}

// IsDeniedAbs reports whether absPath (including symlink resolution) under the
// compiled root is denied to AgentAudience.
func (f AgentReadFloor) IsDeniedAbs(relSlash, absPath string) bool {
	if f.IsDeniedRel(relSlash) {
		return true
	}
	return isAgentSourceReadDeniedWith(f.rootClean, absPath, f.effScratch, f.effOutput, f.unconditionalDenyRoots)
}
