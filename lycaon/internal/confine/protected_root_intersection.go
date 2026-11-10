package confine

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

// underSecretReadDeny reports whether path sits in the control-plane read floor.
// Comparison folds case where the filesystem does — see path_case.go.
func underSecretReadDeny(path string) bool {
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

// defaultRootContainsStateFolder drops an OS default root inside the state tree.
// Cache/temp roots that happen to contain state remain usable because the
// control-plane and ancestor-spine floors protect the state tree inside them.
func defaultRootContainsStateFolder(path string) bool {
	return ControlPlaneWriteDenied(path)
}

// ControlPlaneStrictAncestors returns the strict ancestor directories (above "/")
// along the lexical and canonical spines of every control-plane root so neither
// the state directory nor its parent spine can be unlinked, removed, or renamed
// when a broader ancestor is writable.
func ControlPlaneStrictAncestors() []string {
	roots := controlPlaneReadDenyRoots()
	if len(roots) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.Clean(strings.TrimSpace(p))
		if p == "" || p == "." || p == "/" {
			return
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
		if runtime.GOOS == "darwin" {
			if alias := darwinAliasPath(p); alias != "" && alias != "/" && !seen[alias] {
				seen[alias] = true
				out = append(out, alias)
			}
		}
	}
	for _, raw := range roots {
		cleaned := filepath.Clean(strings.TrimSpace(raw))
		if cleaned == "" || cleaned == "." || !filepath.IsAbs(cleaned) {
			continue
		}
		resolved := fspath.CanonicalPath(cleaned)
		if !PathEqual(cleaned, resolved) {
			add(cleaned)
		}
		if parentCanon := parentCanonicalPath(cleaned); parentCanon != "" && !PathEqual(parentCanon, resolved) {
			add(parentCanon)
		}
		for anc := filepath.Dir(cleaned); anc != "" && anc != "/" && anc != "."; anc = filepath.Dir(anc) {
			add(anc)
			add(parentCanonicalPath(anc))
			add(fspath.CanonicalPath(anc))
			if filepath.Dir(anc) == anc {
				break
			}
		}
		for anc := filepath.Dir(resolved); anc != "" && anc != "/" && anc != "."; anc = filepath.Dir(anc) {
			add(anc)
			if filepath.Dir(anc) == anc {
				break
			}
		}
	}
	return out
}

func parentCanonicalPath(p string) string {
	cleaned := filepath.Clean(strings.TrimSpace(p))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return ""
	}
	parent := fspath.CanonicalPath(filepath.Dir(cleaned))
	if parent == "" {
		return ""
	}
	return filepath.Join(parent, filepath.Base(cleaned))
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
