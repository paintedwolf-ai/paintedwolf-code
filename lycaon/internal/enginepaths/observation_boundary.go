package enginepaths

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fspath"
)

type cachedCfgRoot struct {
	raw   string
	clean string
	canon string
}

var lastCfgRoot atomic.Pointer[cachedCfgRoot]

func currentConfigRoot(resolveCanon bool) (clean, canon string, ok bool) {
	cfgRoot, err := configdir.Root()
	if err != nil || strings.TrimSpace(cfgRoot) == "" {
		return "", "", false
	}
	clean = filepath.Clean(cfgRoot)
	if !resolveCanon {
		if cached := lastCfgRoot.Load(); cached != nil && cached.raw == cfgRoot {
			return cached.clean, cached.canon, true
		}
	}
	canon = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(clean)))
	if canon == "" || canon == "." {
		canon = clean
	}
	lastCfgRoot.Store(&cachedCfgRoot{raw: cfgRoot, clean: clean, canon: canon})
	return clean, canon, true
}

// IsObservedStateExcluded reports whether absPath must be excluded from source
// discovery, directory listing, catalog indexing, and filesystem watch events
// when observed under rootPath.
//
// Rules:
//  1. When rootPath is a strict ancestor of configdir.Root() (or outside it),
//     the entire configdir.Root() tree and worker placement folders are excluded.
//     Explicit managed workspaces (<configdir>/drafts/<id>,
//     <configdir>/session-worktrees/<proj>/<sess>, <configdir>/worktrees/<id>,
//     <configdir>/worker-branches/<proj>/<job>, and
//     <placement>/worker-branches/<proj>/<job>) remain included when rootPath
//     itself is at or under that managed workspace.
//  2. When rootPath is configdir.Root() or a non-workspace descendant of
//     configdir.Root(), host runtime/database/cache/session/secret state under
//     configdir.Root() is excluded (even if rootPath starts inside such a
//     runtime directory), while human-authored configuration files and
//     subdirectories (such as settings.yaml, mcp.yaml, packs/, agents/, etc.)
//     remain observable.
func IsObservedStateExcluded(rootPath, absPath string) bool {
	return isObservedStateExcluded(rootPath, absPath, true)
}

func isObservedStateExcluded(rootPath, absPath string, resolveCanon bool) bool {
	p := filepath.Clean(strings.TrimSpace(absPath))
	if p == "" || p == "." {
		return false
	}
	rootClean := filepath.Clean(strings.TrimSpace(rootPath))
	if rootClean == "." {
		rootClean = ""
	}

	if isPlacementExcludedLexical(rootClean, p) {
		return true
	}
	var rootCanon, pCanon string
	if resolveCanon {
		pCanon = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(p)))
		if rootClean != "" {
			rootCanon = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(rootClean)))
		}
		if pCanon != "" && pCanon != "." && isPlacementExcludedLexical(rootCanon, pCanon) {
			return true
		}
	}

	cfgClean, cfgCanon, ok := currentConfigRoot(resolveCanon)
	if !ok {
		return false
	}

	if excluded, matched := checkConfigRootObservation(cfgClean, rootClean, p); matched && excluded {
		return true
	}
	if cfgCanon != cfgClean {
		if excluded, matched := checkConfigRootObservation(cfgCanon, rootClean, p); matched && excluded {
			return true
		}
	}
	if resolveCanon && pCanon != "" && pCanon != "." {
		effRoot := rootCanon
		if effRoot == "" {
			effRoot = rootClean
		}
		if excluded, matched := checkConfigRootObservation(cfgCanon, effRoot, pCanon); matched && excluded {
			return true
		}
		if cfgClean != cfgCanon {
			if excluded, matched := checkConfigRootObservation(cfgClean, effRoot, pCanon); matched && excluded {
				return true
			}
		}
	}
	return false
}

// IsObservedDirChildExcluded reports whether an immediate child of dirPath
// should be hidden when observing rootPath. When resolveCanon is false, it
// performs pure in-memory checks against the cached config root.
func IsObservedDirChildExcluded(rootPath, dirPath, childName string, resolveCanon bool) bool {
	if childName == "" || childName == "." || childName == ".." {
		return false
	}
	if IsWorkerPlacementDirName(childName) {
		return true
	}
	full := filepath.Join(dirPath, childName)
	if isObservedStateExcluded(rootPath, full, false) {
		return true
	}
	if !resolveCanon {
		return false
	}
	info, err := os.Lstat(full)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return isObservedStateExcluded(rootPath, full, true)
	}
	return false
}

func isPlacementExcludedLexical(rootPath, absPath string) bool {
	placementRoot, rel, ok := splitWorkerPlacementPath(absPath)
	if !ok {
		return false
	}
	wsRel, ok := managedPlacementWorkspaceRel(rel)
	if !ok {
		return true
	}
	if rootPath == "" {
		return false
	}
	wsAbs := filepath.Join(placementRoot, filepath.FromSlash(wsRel))
	_, underWS := relUnderFold(wsAbs, rootPath)
	return !underWS
}

func splitWorkerPlacementPath(absPath string) (placementRoot, relSlash string, ok bool) {
	clean := filepath.Clean(absPath)
	slash := filepath.ToSlash(clean)
	parts := strings.Split(slash, "/")
	for i, part := range parts {
		if IsWorkerPlacementDirName(part) {
			prefix := strings.Join(parts[:i+1], "/")
			if prefix == "" {
				prefix = "/"
			}
			placementRoot = filepath.FromSlash(prefix)
			if i+1 >= len(parts) {
				return placementRoot, ".", true
			}
			return placementRoot, strings.Join(parts[i+1:], "/"), true
		}
	}
	return "", "", false
}

func managedPlacementWorkspaceRel(relSlash string) (string, bool) {
	if relSlash == "" || relSlash == "." {
		return "", false
	}
	segs := strings.Split(relSlash, "/")
	if len(segs) < 2 || !strings.EqualFold(segs[0], WorkerBranchesDirName) || segs[1] == "" {
		return "", false
	}
	if len(segs) >= 3 && segs[2] != "" {
		return segs[0] + "/" + segs[1] + "/" + segs[2], true
	}
	return segs[0] + "/" + segs[1], true
}

func checkConfigRootObservation(cfgBase, rootPath, targetPath string) (excluded, matched bool) {
	rel, underCfg := relUnderFold(cfgBase, targetPath)
	if !underCfg {
		return false, false
	}

	var rootRel string
	var rootUnderCfg bool
	if rootPath != "" {
		rootRel, rootUnderCfg = relUnderFold(cfgBase, rootPath)
	}

	if _, ok := managedConfigWorkspacePrefix(rel); ok {
		if rootPath == "" {
			return false, true
		}
		if rootUnderCfg {
			if _, rootIsWS := managedConfigWorkspacePrefix(rootRel); rootIsWS && sameManagedWorkspace(rel, rootRel) {
				return false, true
			}
		}
		return true, true
	}

	if rootPath != "" {
		if !rootUnderCfg {
			// Strict ancestor or external root with symlink into configdir.Root().
			return true, true
		}
		if _, rootIsWS := managedConfigWorkspacePrefix(rootRel); rootIsWS {
			// Attached root is a managed workspace, but targetPath escaped into
			// another part of configdir.Root().
			return true, true
		}
	}

	if rel == "." {
		return false, true
	}
	firstSeg := strings.SplitN(rel, "/", 2)[0]
	return IsControlRootRuntimeEntry(firstSeg), true
}

func managedConfigWorkspacePrefix(relSlash string) (string, bool) {
	if relSlash == "" || relSlash == "." {
		return "", false
	}
	segs := strings.Split(relSlash, "/")
	if len(segs) < 2 || segs[1] == "" {
		return "", false
	}
	first := strings.ToLower(segs[0])
	switch first {
	case DraftsDirName, "worktrees", SessionWorktreesDirName, WorkerBranchesDirName:
		return segs[0] + "/" + segs[1], true
	default:
		return "", false
	}
}

func sameManagedWorkspace(targetRel, rootRel string) bool {
	if targetRel == "" || targetRel == "." || rootRel == "" || rootRel == "." {
		return false
	}
	targetLower := strings.ToLower(targetRel)
	rootLower := strings.ToLower(rootRel)
	return targetLower == rootLower || strings.HasPrefix(targetLower, rootLower+"/")
}

// IsControlRootRuntimeEntry reports whether a top-level entry name under
// configdir.Root() belongs to host runtime, database, cache, session,
// workspace-container, or secret state rather than human-authored configuration.
func IsControlRootRuntimeEntry(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "" {
		return false
	}
	for _, dbPrefix := range []string{
		"store.db",
		"source-observations.db",
		"web-index.db",
		"paintedwolf.db",
		"source-catalog.db",
	} {
		if lower == dbPrefix || strings.HasPrefix(lower, dbPrefix+"-") || strings.HasPrefix(lower, dbPrefix+".") {
			return true
		}
	}
	for _, prefix := range []string{
		".store-upgrade-",
		".restore-staging",
		".restore-recovery",
		".backup-snapshot-",
		".backup-transfer-",
		".paintedwolf-tmp-",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	switch lower {
	case
		// Store & recovery state
		"upgrade-recovery",
		"restore.pending.json",
		// Localdata & caches
		"browser-cache",
		"cache",
		"debug",
		"decide-models",
		"extensions",
		"extensions-meta",
		"fetch-cache",
		"local-models",
		"modelfeed",
		"osv-cache",
		"pricing-cache",
		"repo-orientation",
		"verify-detect.json",
		// Daemon, IPC, locks, tokens, Tauri, and process state
		"locks",
		"cli.sock",
		"cli-projects.cache",
		"daemon.json",
		"api.token",
		"socket-references",
		"ssh",
		"worker-placements.json",
		"updates",
		"updates.log",
		"window-backdrop.json",
		"conversation-preparation",
		// Durable internal state & managed workspace containers
		"app-state-v1",
		"document-outbox",
		"projects",
		"source-content",
		"worker-baselines",
		"worker-branches",
		"worker-seeds",
		"session-worktrees",
		"worktrees",
		"session-checkpoints",
		"scratch",
		"session-output",
		"drafts",
		// Host secret / vault material
		"credential-vault.age",
		"credential-vault-identity.age",
		"credential-vault-dev.key",
		"host-identity.pem":
		return true
	}
	return false
}

// AgentReadFloor is a precompiled per-root predicate that filters out
// control-plane state and read-floor-denied paths (such as key material)
// for AgentAudience views without per-path syscalls.
type AgentReadFloor struct {
	rootClean          string
	rootCanon          string
	whollyDenied       bool
	deniedRelPrefixes  []string
	inPlacementBranch  bool
}

// CompileAgentReadFloor compiles the agent read floor for rootPath given
// additional read-denied directory roots (such as key-material store paths).
func CompileAgentReadFloor(rootPath string, extraDenyRoots []string) AgentReadFloor {
	clean := filepath.Clean(strings.TrimSpace(rootPath))
	if clean == "" || clean == "." {
		return AgentReadFloor{}
	}
	canon := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(clean)))
	if canon == "" || canon == "." {
		canon = clean
	}
	floor := AgentReadFloor{
		rootClean: clean,
		rootCanon: canon,
	}

	if isPlacementExcludedLexical(clean, clean) || isPlacementExcludedLexical(canon, canon) {
		floor.whollyDenied = true
		return floor
	}
	if _, _, ok := splitWorkerPlacementPath(clean); ok {
		floor.inPlacementBranch = true
	} else if _, _, ok := splitWorkerPlacementPath(canon); ok {
		floor.inPlacementBranch = true
	}

	var denyRoots []string
	if cfgClean, cfgCanon, ok := currentConfigRoot(true); ok {
		if isRootDeniedByConfigTree(cfgClean, clean) || isRootDeniedByConfigTree(cfgCanon, canon) {
			floor.whollyDenied = true
			return floor
		}
		denyRoots = append(denyRoots, cfgClean)
		if cfgCanon != cfgClean {
			denyRoots = append(denyRoots, cfgCanon)
		}
	}

	for _, extra := range extraDenyRoots {
		trimmed := strings.TrimSpace(extra)
		if trimmed == "" {
			continue
		}
		extraClean := filepath.Clean(trimmed)
		denyRoots = append(denyRoots, extraClean)
		if extraCanon := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(extraClean))); extraCanon != "" && extraCanon != "." && extraCanon != extraClean {
			denyRoots = append(denyRoots, extraCanon)
		}
	}

	seen := make(map[string]struct{})
	for _, deny := range denyRoots {
		if _, underDeny := relUnderFold(deny, clean); underDeny {
			// If root is inside configdir.Root(), isRootDeniedByConfigTree already
			// allowed only explicit managed workspaces; do not mark a managed
			// workspace wholly denied because its ancestor is configdir.Root().
			if !isManagedWorkspaceRoot(clean, canon) {
				floor.whollyDenied = true
				return floor
			}
			continue
		}
		if _, underDeny := relUnderFold(deny, canon); underDeny {
			if !isManagedWorkspaceRoot(clean, canon) {
				floor.whollyDenied = true
				return floor
			}
			continue
		}
		for _, base := range []string{clean, canon} {
			if rel, ok := relUnderFold(base, deny); ok && rel != "." {
				lower := strings.ToLower(rel)
				if _, exists := seen[lower]; !exists {
					seen[lower] = struct{}{}
					floor.deniedRelPrefixes = append(floor.deniedRelPrefixes, lower)
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
	if !f.inPlacementBranch && HasWorkerPlacementSegment(clean) {
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
func (f AgentReadFloor) IsDeniedAbs(relSlash, absPath string, extraDenyRoots []string) bool {
	if f.IsDeniedRel(relSlash) {
		return true
	}
	return IsAgentSourceReadDenied(f.rootClean, absPath, extraDenyRoots)
}

// IsAgentSourceReadDenied reports whether absPath under rootPath is denied to
// AgentAudience (either because it is excluded from observation, lies inside
// the control-plane state tree outside an attached managed workspace, or lies
// inside any of extraDenyRoots).
func IsAgentSourceReadDenied(rootPath, absPath string, extraDenyRoots []string) bool {
	if IsObservedStateExcluded(rootPath, absPath) {
		return true
	}
	p := filepath.Clean(strings.TrimSpace(absPath))
	if p == "" || p == "." {
		return false
	}
	pCanon := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(p)))
	if pCanon == "" || pCanon == "." {
		pCanon = p
	}
	rootClean := filepath.Clean(strings.TrimSpace(rootPath))
	var rootCanon string
	if rootClean != "" && rootClean != "." {
		rootCanon = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(rootClean)))
	}

	if cfgClean, cfgCanon, ok := currentConfigRoot(true); ok {
		if isPathDeniedInConfigTreeForAgent(cfgClean, rootClean, p) ||
			isPathDeniedInConfigTreeForAgent(cfgCanon, rootCanon, pCanon) ||
			isPathDeniedInConfigTreeForAgent(cfgCanon, rootClean, pCanon) {
			return true
		}
	}

	for _, extra := range extraDenyRoots {
		trimmed := strings.TrimSpace(extra)
		if trimmed == "" {
			continue
		}
		extraClean := filepath.Clean(trimmed)
		if _, under := relUnderFold(extraClean, p); under {
			return true
		}
		if _, under := relUnderFold(extraClean, pCanon); under {
			return true
		}
		if extraCanon := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(extraClean))); extraCanon != "" && extraCanon != "." {
			if _, under := relUnderFold(extraCanon, p); under {
				return true
			}
			if _, under := relUnderFold(extraCanon, pCanon); under {
				return true
			}
		}
	}
	return false
}

func isRootDeniedByConfigTree(cfgBase, rootPath string) bool {
	rel, under := relUnderFold(cfgBase, rootPath)
	if !under {
		return false
	}
	_, isWS := managedConfigWorkspacePrefix(rel)
	return !isWS
}

func isManagedWorkspaceRoot(clean, canon string) bool {
	cfgClean, cfgCanon, ok := currentConfigRoot(true)
	if !ok {
		return false
	}
	if rel, under := relUnderFold(cfgClean, clean); under {
		if _, isWS := managedConfigWorkspacePrefix(rel); isWS {
			return true
		}
	}
	if rel, under := relUnderFold(cfgCanon, canon); under {
		if _, isWS := managedConfigWorkspacePrefix(rel); isWS {
			return true
		}
	}
	return false
}

func isPathDeniedInConfigTreeForAgent(cfgBase, rootPath, targetPath string) bool {
	if cfgBase == "" || targetPath == "" {
		return false
	}
	rel, underCfg := relUnderFold(cfgBase, targetPath)
	if !underCfg {
		return false
	}
	if _, isWS := managedConfigWorkspacePrefix(rel); !isWS {
		// All of configdir.Root() outside explicit managed workspaces (including
		// settings.yaml, packs/, etc.) is denied to AgentAudience!
		return true
	}
	if rootPath == "" || rootPath == "." {
		return false
	}
	rootRel, rootUnderCfg := relUnderFold(cfgBase, rootPath)
	if !rootUnderCfg {
		return true
	}
	return !sameManagedWorkspace(rel, rootRel)
}

func relUnderFold(base, target string) (string, bool) {
	if base == "" || target == "" {
		return "", false
	}
	baseSlash := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(base)), "/")
	targetSlash := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(target)), "/")
	if baseSlash == "" {
		if strings.HasPrefix(targetSlash, "/") {
			rel := strings.TrimPrefix(targetSlash, "/")
			if rel == "" {
				return ".", true
			}
			return rel, true
		}
		return "", false
	}
	if strings.EqualFold(baseSlash, targetSlash) {
		return ".", true
	}
	if len(targetSlash) > len(baseSlash) && targetSlash[len(baseSlash)] == '/' && strings.EqualFold(targetSlash[:len(baseSlash)], baseSlash) {
		return targetSlash[len(baseSlash)+1:], true
	}
	return "", false
}
