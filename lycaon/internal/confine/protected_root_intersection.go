package confine

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
)

// underSecretReadDeny reports whether path sits in the control-plane read floor.
// Comparison folds case where the filesystem does — see path_case.go.
func underSecretReadDeny(path string) bool {
	if underAny(path, configuredReadDenyRoots()) {
		return true
	}
	if underSecretReadAllowBack(path) {
		return false
	}
	return underAny(path, SecretReadDenyRoots())
}

func underSecretReadAllowBack(path string) bool {
	return underAny(path, SecretReadAllowBackRoots())
}

// Store descendants stay protected within an admitted standing root.
func attachedWriteRootForbidden(path string) bool {
	return underSecretReadDeny(path) || underAny(path, KeyMaterialWritePaths()) || underAny(path, CredentialStorePaths())
}

// NormalizeWriteRootKey returns a stable deny-set / pending-map key for a write root.
func NormalizeWriteRootKey(root string) string {
	return filepath.Clean(strings.TrimSpace(root))
}

// defaultRootContainsStateFolder drops an OS default root above the state folder.
// A placement folder under one is denied by HardDenyWriteSpecs instead, because
// losing /var/tmp as a write root would break ordinary builds.
func defaultRootContainsStateFolder(path string) bool {
	dir, err := configdir.Root()
	if err != nil || strings.TrimSpace(dir) == "" {
		return false
	}
	return PathAtOrUnder(fspath.CanonicalPath(dir), path)
}

func defaultRootAbovePlacementOnly(path string) bool {
	if defaultRootContainsStateFolder(path) {
		return false
	}
	for _, root := range osDefaultWriteRoots() {
		if fspath.CanonicalPath(root) == path {
			return true
		}
	}
	return false
}

// writeRootContainsStateTree prevents an ancestor rename from moving host state.
func writeRootContainsStateTree(path string) bool {
	for _, state := range resolveEach(controlPlaneReadDenyRoots()) {
		if PathAtOrUnder(state, path) {
			return true
		}
	}
	return false
}

// IsAgentSourceReadDenied reports whether absPath under rootPath is denied to
// AgentAudience, optionally scoped with the invocation's own sessionScratch and
// sessionOutput directories.
func IsAgentSourceReadDenied(rootPath, absPath string, sessionDirs ...string) bool {
	rawScratch, rawOutput := parseSessionDirs(sessionDirs)
	effScratch, effOutput := rawScratch, rawOutput
	if strings.TrimSpace(rootPath) != "" {
		effScratch, effOutput = EffectiveSessionDirsForRoot(rootPath, rawScratch, rawOutput)
	}
	return isAgentSourceReadDeniedWith(rootPath, absPath, effScratch, effOutput, unconditionalAgentDenyRoots())
}

// IsAgentControlStateReadDenied reports whether absPath under rootPath is
// denied by control-plane state, observed engine state exclusions, or
// configured LYCAON_SANDBOX_DENY_READ roots (independent of key-material
// paths, which may be opened for an exact approved single-file read).
func IsAgentControlStateReadDenied(rootPath, absPath string, sessionDirs ...string) bool {
	rawScratch, rawOutput := parseSessionDirs(sessionDirs)
	effScratch, effOutput := rawScratch, rawOutput
	if strings.TrimSpace(rootPath) != "" {
		effScratch, effOutput = EffectiveSessionDirsForRoot(rootPath, rawScratch, rawOutput)
	}
	return isAgentSourceReadDeniedWith(rootPath, absPath, effScratch, effOutput, configuredReadDenyRoots())
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
		if ControlPlanePathDenied(p, false, effScratch, effOutput) ||
			ControlPlanePathDenied(pCanon, false, effScratch, effOutput) {
			return true
		}
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
