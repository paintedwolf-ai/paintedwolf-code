package confine

import (
	"path/filepath"
	"strings"
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

// attachedWriteRootForbidden checks both path directions: standing roots must
// not be, contain, or sit inside a protected store.
func attachedWriteRootForbidden(path string) bool {
	return intersectsSecretReadDeny(path) || intersectsKeyMaterial(path) || intersectsCredentialStore(path)
}

// intersectsSecretReadDeny checks both directions after workspace carve-outs.
func intersectsSecretReadDeny(path string) bool {
	if underSecretReadAllowBack(path) {
		return false
	}
	if underSecretReadDeny(path) {
		return true
	}
	for _, deny := range SecretReadDenyRoots() {
		if strings.TrimSpace(deny) == "" {
			continue
		}
		if PathStrictlyUnder(deny, path) {
			return true
		}
	}
	return false
}

// NormalizeWriteRootKey returns a stable deny-set / pending-map key for a write root.
func NormalizeWriteRootKey(root string) string {
	return filepath.Clean(strings.TrimSpace(root))
}
