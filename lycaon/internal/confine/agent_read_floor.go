package confine

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
)

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

// IsAgentSourceReadDenied reports whether absPath under rootPath is denied to
// AgentAudience, optionally scoped with the invocation's own sessionScratch and
// sessionOutput directories.
func IsAgentSourceReadDenied(rootPath, absPath string, sessionDirs ...string) bool {
	rawScratch, rawOutput := parseSessionDirs(sessionDirs)
	effScratch, effOutput := EffectiveSessionDirsForRoot(rootPath, rawScratch, rawOutput)
	return isAgentSourceReadDeniedWith(rootPath, absPath, effScratch, effOutput, unconditionalAgentDenyRoots())
}

func isAgentSourceReadDeniedWith(rootPath, absPath, effScratch, effOutput string, unconditionalDenyRoots []string) bool {
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

	// 1. Unconditional extra read-deny roots and key-material roots apply to
	// both rootPath and absPath without managed-workspace or session carve-outs.
	for _, extra := range unconditionalDenyRoots {
		for _, denyVariant := range pathVariants(extra) {
			if _, under := enginepaths.RelUnderFold(denyVariant, p); under {
				return true
			}
			if _, under := enginepaths.RelUnderFold(denyVariant, pCanon); under {
				return true
			}
			if rootClean != "" && rootClean != "." {
				if _, under := enginepaths.RelUnderFold(denyVariant, rootClean); under {
					return true
				}
			}
			if rootCanon != "" && rootCanon != "." {
				if _, under := enginepaths.RelUnderFold(denyVariant, rootCanon); under {
					return true
				}
			}
		}
	}

	inOwnedSession := (effScratch != "" && WithinSessionDirectory(rootClean, effScratch) && WithinSessionDirectory(p, effScratch)) ||
		(effOutput != "" && WithinSessionDirectory(rootClean, effOutput) && WithinSessionDirectory(p, effOutput))
	if inOwnedSession {
		return false
	}

	if enginepaths.IsObservedStateExcluded(rootPath, absPath) {
		return true
	}

	if !secretReadDenyDisabled() {
		if cfgClean, cfgCanon, ok := enginepaths.CurrentConfigRoot(true); ok {
			rootVariants := []string{rootClean}
			if rootCanon != "" && rootCanon != "." && rootCanon != rootClean {
				rootVariants = append(rootVariants, rootCanon)
			}
			if isPathDeniedInConfigTreeForAgent(cfgClean, rootVariants, p) ||
				isPathDeniedInConfigTreeForAgent(cfgCanon, rootVariants, pCanon) ||
				isPathDeniedInConfigTreeForAgent(cfgCanon, rootVariants, p) {
				return true
			}
		}
	}
	return false
}

func pathVariants(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	clean := filepath.Clean(trimmed)
	canon := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(clean)))
	if canon != "" && canon != "." && canon != clean {
		return []string{clean, canon}
	}
	return []string{clean}
}

func isRootDeniedByConfigTree(cfgBase, rootPath string) bool {
	rel, under := enginepaths.RelUnderFold(cfgBase, rootPath)
	if !under {
		return false
	}
	_, isWS := enginepaths.ManagedConfigWorkspacePrefix(rel)
	return !isWS
}

func isPathDeniedInConfigTreeForAgent(cfgBase string, rootVariants []string, targetPath string) bool {
	if cfgBase == "" || targetPath == "" {
		return false
	}
	rel, underCfg := enginepaths.RelUnderFold(cfgBase, targetPath)
	if !underCfg {
		return false
	}
	if _, isWS := enginepaths.ManagedConfigWorkspacePrefix(rel); !isWS {
		return true
	}
	hasRoot := false
	var rootRel string
	var rootUnderCfg bool
	for _, r := range rootVariants {
		if r == "" || r == "." {
			continue
		}
		hasRoot = true
		if rr, ok := enginepaths.RelUnderFold(cfgBase, r); ok {
			rootRel = rr
			rootUnderCfg = true
			break
		}
	}
	if !hasRoot {
		return false
	}
	if !rootUnderCfg {
		return true
	}
	return !enginepaths.SameManagedConfigWorkspace(rel, rootRel)
}
