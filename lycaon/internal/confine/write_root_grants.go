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
	WriteRootCodeNotAbsolute   = "write_root_not_absolute"
	WriteRootCodeSecretStore   = "write_root_under_secret_store"
	WriteRootCodeControlPlane  = "write_root_under_control_plane"
	WriteRootCodeContainsState = "write_root_contains_control_plane"
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
func ControlPlanePathDenied(path string, write bool, sessionScratch, sessionOutput string) bool {
	if write && (writeRootContainsStateTree(path) || writeRootContainsStateTree(fspath.CanonicalPath(path))) {
		return true
	}
	if !write && underAny(fspath.CanonicalPath(path), configuredReadDenyRoots()) {
		return true
	}
	if WithinSessionDirectory(path, sessionScratch) || (!write && WithinSessionDirectory(path, sessionOutput)) {
		return false
	}
	if write {
		return ControlPlaneTree(path)
	}
	return ControlPlaneReadDenied(path)
}

// WithinSessionDirectory reports whether path lies in a session folder.
func WithinSessionDirectory(path, directory string) bool {
	root := filepath.Clean(strings.TrimSpace(directory))
	p := filepath.Clean(strings.TrimSpace(path))
	if root == "." || !filepath.IsAbs(root) || p == "." || !filepath.IsAbs(p) {
		return false
	}
	return PathAtOrUnder(fspath.CanonicalPath(p), fspath.CanonicalPath(root))
}

// EffectiveSessionDirsForRoot resolves the session scratch and output
// directories that apply when evaluating read-floor access under root.
// Explicit session directories on the invocation context only carve out a root
// that is itself within that session directory (never configdir.Root(), $HOME,
// or another session's folder). Concrete session path shape alone never
// confers read authority without explicit invocation ownership.
func EffectiveSessionDirsForRoot(root, sessionScratch, sessionOutput string) (string, string) {
	p := filepath.Clean(strings.TrimSpace(root))
	if p == "" || p == "." || !filepath.IsAbs(p) {
		return "", ""
	}
	scratch := strings.TrimSpace(sessionScratch)
	output := strings.TrimSpace(sessionOutput)
	var effScratch, effOutput string
	if scratch != "" && WithinSessionDirectory(p, scratch) {
		effScratch = scratch
	}
	if output != "" && WithinSessionDirectory(p, output) {
		effOutput = output
	}
	return effScratch, effOutput
}

// RootOverlapsControlPlaneReadFloor reports whether root lies within or is an
// ancestor of any control-plane read-deny root, so callers can skip per-entry
// canonicalization when walking ordinary roots that neither overlap nor contain
// the control plane.
func RootOverlapsControlPlaneReadFloor(root string) bool {
	p := filepath.Clean(strings.TrimSpace(root))
	if p == "" || p == "." {
		return false
	}
	pCanon := fspath.CanonicalPath(p)
	if pCanon == "" {
		pCanon = p
	}
	for _, deny := range SecretReadDenyRoots() {
		if PathAtOrUnder(pCanon, deny) || PathAtOrUnder(deny, pCanon) {
			return true
		}
		if PathAtOrUnder(p, deny) || PathAtOrUnder(deny, p) {
			return true
		}
	}
	return false
}

// ControlPlaneReadDenied refuses active host reads outside managed workspaces.
func ControlPlaneReadDenied(root string) bool {
	p := filepath.Clean(strings.TrimSpace(root))
	if p == "" || p == "." {
		return false
	}
	return underSecretReadDeny(fspath.CanonicalPath(p))
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

// IsAgentControlStateReadDenied reports whether absPath under rootPath is
// denied by control-plane state or configured LYCAON_SANDBOX_DENY_READ roots
// (independent of key-material paths, which may be opened for an exact approved
// single-file read).
func IsAgentControlStateReadDenied(rootPath, absPath string, sessionDirs ...string) bool {
	rawScratch, rawOutput := parseSessionDirs(sessionDirs)
	effScratch, effOutput := rawScratch, rawOutput
	if strings.TrimSpace(rootPath) != "" {
		effScratch, effOutput = EffectiveSessionDirsForRoot(rootPath, rawScratch, rawOutput)
	}
	return ControlPlanePathDenied(absPath, false, effScratch, effOutput) ||
		ControlPlanePathDenied(fspath.CanonicalPath(absPath), false, effScratch, effOutput)
}

// IsAgentSourceReadDenied reports whether absPath under rootPath is denied to
// agent reads without an exact single-file read grant, optionally scoped with
// the invocation's own sessionScratch and sessionOutput directories.
func IsAgentSourceReadDenied(rootPath, absPath string, sessionDirs ...string) bool {
	if IsAgentControlStateReadDenied(rootPath, absPath, sessionDirs...) {
		return true
	}
	p := filepath.Clean(strings.TrimSpace(absPath))
	if p == "" || p == "." {
		return false
	}
	deny := KeyMaterialReadDenyPaths()
	if underAny(p, deny) || underAny(fspath.CanonicalPath(p), deny) {
		return true
	}
	if r := filepath.Clean(strings.TrimSpace(rootPath)); r != "" && r != "." {
		if underAny(r, deny) || underAny(fspath.CanonicalPath(r), deny) {
			return true
		}
	}
	return false
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

// AttachedWriteRootRefused validates standing write roots structurally;
// per-path confinement floors govern protected descendants and the control plane.
func AttachedWriteRootRefused(root string) (refused bool, code string) {
	p, structuralCode := writeRootStructuralRefusal(root)
	if structuralCode != "" {
		return true, structuralCode
	}
	if fspath.CanonicalPath(p) == "" {
		return true, WriteRootCodeNotAbsolute
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
	if resolved == "" {
		return true, WriteRootCodeNotAbsolute
	}
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
