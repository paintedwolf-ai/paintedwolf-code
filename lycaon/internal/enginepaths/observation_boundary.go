package enginepaths

import (
	"os"
	"path/filepath"
	"runtime"
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

// CurrentConfigRoot returns the cleaned lexical and canonical paths of the
// active configdir.Root().
func CurrentConfigRoot(resolveCanon bool) (clean, canon string, ok bool) {
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

	if IsPlacementExcludedLexical(rootClean, p) {
		return true
	}
	var rootCanon, pCanon string
	if resolveCanon {
		pCanon = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(p)))
		if rootClean != "" {
			rootCanon = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(rootClean)))
		}
		if pCanon != "" && pCanon != "." && IsPlacementExcludedLexical(rootCanon, pCanon) {
			return true
		}
	}

	cfgClean, cfgCanon, ok := CurrentConfigRoot(resolveCanon)
	if !ok {
		return false
	}

	rootVariants := []string{rootClean}
	if rootCanon != "" && rootCanon != "." && rootCanon != rootClean {
		rootVariants = append(rootVariants, rootCanon)
	}

	if excluded, matched := checkConfigRootObservation(cfgClean, rootVariants, p); matched && excluded {
		return true
	}
	if cfgCanon != cfgClean {
		if excluded, matched := checkConfigRootObservation(cfgCanon, rootVariants, p); matched && excluded {
			return true
		}
	}
	if resolveCanon && pCanon != "" && pCanon != "." {
		if excluded, matched := checkConfigRootObservation(cfgCanon, rootVariants, pCanon); matched && excluded {
			return true
		}
		if cfgClean != cfgCanon {
			if excluded, matched := checkConfigRootObservation(cfgClean, rootVariants, pCanon); matched && excluded {
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

// IsPlacementExcludedLexical reports whether absPath lies inside a worker
// placement folder outside an explicitly attached placement branch root.
func IsPlacementExcludedLexical(rootPath, absPath string) bool {
	placementRoot, rel, ok := SplitWorkerPlacementPath(absPath)
	if !ok {
		return false
	}
	wsRel, ok := ManagedPlacementWorkspaceRel(rel)
	if !ok {
		return true
	}
	if rootPath == "" {
		return false
	}
	wsAbs := filepath.Join(placementRoot, filepath.FromSlash(wsRel))
	_, underWS := RelUnderFold(wsAbs, rootPath)
	return !underWS
}

// SplitWorkerPlacementPath splits absPath at a worker placement folder segment.
func SplitWorkerPlacementPath(absPath string) (placementRoot, relSlash string, ok bool) {
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

// HasWorkerPlacementSegment reports whether relSlash contains a path segment
// matching WorkerPlacementDirPrefix.
func HasWorkerPlacementSegment(relSlash string) bool {
	for _, seg := range strings.Split(strings.ReplaceAll(relSlash, "\\", "/"), "/") {
		if IsWorkerPlacementDirName(seg) {
			return true
		}
	}
	return false
}

// ManagedPlacementWorkspaceRel returns the concrete worker-branch workspace
// prefix under a placement folder when relSlash lies inside one.
func ManagedPlacementWorkspaceRel(relSlash string) (string, bool) {
	if relSlash == "" || relSlash == "." {
		return "", false
	}
	segs := strings.Split(relSlash, "/")
	if len(segs) < 2 || !strings.EqualFold(segs[0], WorkerBranchesDirName) || segs[1] == "" {
		return "", false
	}
	if len(segs) >= 3 && segs[2] != "" {
		if IsJobMetaDirName(segs[2]) {
			return "", false
		}
		return segs[0] + "/" + segs[1] + "/" + segs[2], true
	}
	return segs[0] + "/" + segs[1], true
}

func checkConfigRootObservation(cfgBase string, rootVariants []string, targetPath string) (excluded, matched bool) {
	rel, underCfg := RelUnderFold(cfgBase, targetPath)
	if !underCfg {
		return false, false
	}

	hasRoot := false
	var rootRel string
	var rootUnderCfg bool
	for _, r := range rootVariants {
		if r == "" || r == "." {
			continue
		}
		hasRoot = true
		if rr, ok := RelUnderFold(cfgBase, r); ok {
			rootRel = rr
			rootUnderCfg = true
			break
		}
	}

	if _, ok := ManagedConfigWorkspacePrefix(rel); ok {
		if !hasRoot {
			return false, true
		}
		if rootUnderCfg {
			if _, rootIsWS := ManagedConfigWorkspacePrefix(rootRel); rootIsWS && SameManagedConfigWorkspace(rel, rootRel) {
				return false, true
			}
		}
		return true, true
	}

	if hasRoot {
		if !rootUnderCfg {
			// Strict ancestor or external root with symlink into configdir.Root().
			return true, true
		}
		if _, rootIsWS := ManagedConfigWorkspacePrefix(rootRel); rootIsWS {
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

// ManagedConfigWorkspacePrefix returns the concrete managed workspace prefix
// under configdir.Root() (drafts/<id>, worktrees/<id>,
// session-worktrees/<proj>/<sess>, or worker-branches/<proj>/<job>, excluding
// .meta directories) when relSlash lies inside one.
func ManagedConfigWorkspacePrefix(relSlash string) (string, bool) {
	if relSlash == "" || relSlash == "." {
		return "", false
	}
	segs := strings.Split(relSlash, "/")
	if len(segs) < 2 || segs[1] == "" || segs[1] == "." || segs[1] == ".." {
		return "", false
	}
	first := strings.ToLower(segs[0])
	switch first {
	case DraftsDirName, "worktrees", SessionWorktreesDirName, WorkerBranchesDirName:
		if first == WorkerBranchesDirName && len(segs) >= 3 && IsJobMetaDirName(segs[2]) {
			return "", false
		}
		return segs[0] + "/" + segs[1], true
	default:
		return "", false
	}
}

// SameManagedConfigWorkspace reports whether targetRel lies inside the same
// concrete managed workspace under configdir.Root() as rootRel and is at or
// under rootRel.
func SameManagedConfigWorkspace(targetRel, rootRel string) bool {
	if targetRel == "" || targetRel == "." || rootRel == "" || rootRel == "." {
		return false
	}
	targetPrefix, okTarget := ManagedConfigWorkspacePrefix(targetRel)
	rootPrefix, okRoot := ManagedConfigWorkspacePrefix(rootRel)
	if !okTarget || !okRoot || !strings.EqualFold(targetPrefix, rootPrefix) {
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
		"daemon.lock",
		"runtime.json",
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
		"logs",
		"sessions",
		"staging",
		"blobs",
		"tls",
		"work",
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
		"auth.json",
		"secrets",
		"master.key",
		"credential-vault.age",
		"credential-vault-identity.age",
		"credential-vault-dev.key",
		"host-identity.pem":
		return true
	}
	return false
}

// RelUnderFold returns the slash-separated path of target relative to base
// using case-insensitive component matching and Darwin /private alias handling.
func RelUnderFold(base, target string) (string, bool) {
	if base == "" || target == "" {
		return "", false
	}
	baseSlash := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(base)), "/")
	targetSlash := strings.TrimSuffix(filepath.ToSlash(filepath.Clean(target)), "/")
	if rel, ok := relUnderFoldSlash(baseSlash, targetSlash); ok {
		return rel, true
	}
	if runtime.GOOS == "darwin" {
		if altBase := darwinPrivateToggle(baseSlash); altBase != "" {
			if rel, ok := relUnderFoldSlash(altBase, targetSlash); ok {
				return rel, true
			}
		}
	}
	return "", false
}

func relUnderFoldSlash(baseSlash, targetSlash string) (string, bool) {
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

func darwinPrivateToggle(slash string) string {
	for _, sys := range []string{"/var", "/tmp", "/etc"} {
		if strings.EqualFold(slash, sys) || (len(slash) > len(sys) && slash[len(sys)] == '/' && strings.EqualFold(slash[:len(sys)], sys)) {
			return "/private" + slash
		}
		priv := "/private" + sys
		if strings.EqualFold(slash, priv) || (len(slash) > len(priv) && slash[len(priv)] == '/' && strings.EqualFold(slash[:len(priv)], priv)) {
			return slash[len("/private"):]
		}
	}
	return ""
}
