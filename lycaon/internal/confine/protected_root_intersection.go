package confine

import (
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/configdir"
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

// writeRootContainsStateTree prevents an ancestor rename from moving host state.
func writeRootContainsStateTree(path string) bool {
	for _, state := range resolveEach(controlPlaneReadDenyRoots()) {
		if PathAtOrUnder(state, path) {
			return true
		}
	}
	return false
}
