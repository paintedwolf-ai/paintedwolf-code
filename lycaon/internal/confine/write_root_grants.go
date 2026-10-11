package confine

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/fspath"
)

// grantedWriteRootsSource supplies live durable write-root grants.
var grantedWriteRootsSource atomic.Pointer[func(projectID string) []string]

// SetGrantedWriteRootsSource installs the durable write_root grant source.
func SetGrantedWriteRootsSource(fn func(projectID string) []string) {
	if fn == nil {
		grantedWriteRootsSource.Store(nil)
		return
	}
	grantedWriteRootsSource.Store(&fn)
}

func grantedWriteRoots(projectID string) []string {
	fn := grantedWriteRootsSource.Load()
	if fn == nil {
		return nil
	}
	return (*fn)(projectID)
}

// Write-root validation codes are shared by every entry point.
const (
	WriteRootCodeFilesystemRoot = "write_root_is_filesystem_root"
	WriteRootCodeHome           = "write_root_is_home"
	WriteRootCodeNotAbsolute    = "write_root_not_absolute"
	WriteRootCodeSecretStore    = "write_root_under_secret_store"
	WriteRootCodeControlPlane   = "write_root_under_control_plane"
)

// ControlPlaneTree reports whether path lies in the host's own state tree,
// outside the agent workspaces the engine manages there. The tree is a fact of
// the install; the read-floor switch decides only whether reads of it are refused.
func ControlPlaneTree(path string) bool {
	p := filepath.Clean(strings.TrimSpace(path))
	if p == "" || p == "." {
		return false
	}
	p = fspath.CanonicalPath(p)
	if underAny(p, resolveEach(agentWorkspaceRootsUnderControlPlane())) {
		return false
	}
	return underAny(p, resolveEach(controlPlaneReadDenyRoots()))
}

// ControlPlanePathDenied is the one verdict the approval gate and the path
// resolver share, so a denied path neither mints a card nor resolves through a grant.
// sessionScratch is the invocation's own scratch root; like the confinement floor,
// the verdict leaves it to the invocation and still denies every other session's.
func ControlPlanePathDenied(path string, write bool, sessionScratch string) bool {
	if WithinSessionScratch(path, sessionScratch) {
		return false
	}
	if write {
		return ControlPlaneTree(path)
	}
	return ControlPlaneReadDenied(path)
}

// WithinSessionScratch reports whether path is sessionScratch or lies beneath it.
func WithinSessionScratch(path, sessionScratch string) bool {
	scratch := filepath.Clean(strings.TrimSpace(sessionScratch))
	p := filepath.Clean(strings.TrimSpace(path))
	if scratch == "." || !filepath.IsAbs(scratch) || p == "." || !filepath.IsAbs(p) {
		return false
	}
	return PathAtOrUnder(fspath.CanonicalPath(p), fspath.CanonicalPath(scratch))
}

// ControlPlaneReadDenied refuses active host reads outside managed workspaces.
func ControlPlaneReadDenied(root string) bool {
	p := filepath.Clean(strings.TrimSpace(root))
	if p == "" || p == "." {
		return false
	}
	p = fspath.CanonicalPath(p)
	if underSecretReadAllowBack(p) {
		return false
	}
	return underSecretReadDeny(p)
}

// ControlPlaneWriteDenied refuses writes to active host inputs. The state tree
// stays write-denied when the read floor is switched off.
func ControlPlaneWriteDenied(root string) bool {
	p := filepath.Clean(strings.TrimSpace(root))
	if p == "" || p == "." {
		return false
	}
	p = fspath.CanonicalPath(p)
	if ControlPlaneTree(p) || ControlPlaneReadDenied(p) {
		return true
	}
	exe, err := os.Executable()
	return err == nil && PathEqual(p, fspath.CanonicalPath(exe))
}

// AttachedWriteRootRefused validates standing write roots.
func AttachedWriteRootRefused(root string) (refused bool, code string) {
	p, structuralCode := writeRootStructuralRefusal(root)
	if structuralCode != "" {
		return true, structuralCode
	}
	// Resolve aliases before comparing protected roots.
	resolved := fspath.CanonicalPath(p)
	if attachedWriteRootForbidden(resolved) {
		return true, WriteRootCodeSecretStore
	}
	return false, ""
}

// GrantedWriteRootRefused validates reviewed write-root leases.
func GrantedWriteRootRefused(root string) (refused bool, code string) {
	p, structuralCode := writeRootStructuralRefusal(root)
	if structuralCode != "" {
		return true, structuralCode
	}
	resolved := fspath.CanonicalPath(p)
	if ControlPlaneWriteDenied(resolved) {
		return true, WriteRootCodeControlPlane
	}
	if ClassifyBlockedWrite(resolved).Kind != WriteSubjectOrdinary {
		return true, WriteRootCodeSecretStore
	}
	return false, ""
}

// writeRootStructuralRefusal rejects malformed roots.
func writeRootStructuralRefusal(root string) (cleaned, code string) {
	p := filepath.Clean(strings.TrimSpace(root))
	if p == "" || p == "." {
		return "", WriteRootCodeNotAbsolute
	}
	if !filepath.IsAbs(p) && !isWindowsAbsPath(p) {
		return "", WriteRootCodeNotAbsolute
	}
	return p, ""
}
