package confine

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
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
// or another session's folder). When no session directories are present on the
// context, a root that is an explicit per-session child under
// <configdir>/scratch/<sessionID> or <configdir>/session-output/<sessionID>
// carves out only that single session's subtree.
func EffectiveSessionDirsForRoot(root, sessionScratch, sessionOutput string) (string, string) {
	p := filepath.Clean(strings.TrimSpace(root))
	if p == "" || p == "." || !filepath.IsAbs(p) {
		return "", ""
	}
	scratch := strings.TrimSpace(sessionScratch)
	output := strings.TrimSpace(sessionOutput)
	if scratch != "" || output != "" {
		var effScratch, effOutput string
		if scratch != "" && WithinSessionDirectory(p, scratch) {
			effScratch = scratch
		}
		if output != "" && WithinSessionDirectory(p, output) {
			effOutput = output
		}
		return effScratch, effOutput
	}
	cfgDir, err := configdir.Root()
	if err != nil || strings.TrimSpace(cfgDir) == "" {
		return "", ""
	}
	pCanon := fspath.CanonicalPath(p)
	cfgCanon := fspath.CanonicalPath(cfgDir)
	if pCanon == "" || cfgCanon == "" {
		return "", ""
	}
	if sid := singleSessionSubdirID(pCanon, enginepaths.ScratchRootUnder(cfgCanon)); sid != "" {
		return enginepaths.SessionScratchUnder(cfgCanon, sid), ""
	}
	if sid := singleSessionSubdirID(pCanon, enginepaths.SessionOutputRootUnder(cfgCanon)); sid != "" {
		return "", enginepaths.SessionOutputUnder(cfgCanon, sid)
	}
	return "", ""
}

func singleSessionSubdirID(targetCanon, containerCanon string) string {
	containerCanon = filepath.Clean(strings.TrimSpace(containerCanon))
	if containerCanon == "" || containerCanon == "." {
		return ""
	}
	if !PathAtOrUnder(targetCanon, containerCanon) || PathEqual(targetCanon, containerCanon) {
		return ""
	}
	rel, err := filepath.Rel(containerCanon, targetCanon)
	if err != nil {
		return ""
	}
	slash := strings.Trim(filepath.ToSlash(filepath.Clean(rel)), "/")
	if slash == "" || slash == "." || slash == ".." || strings.HasPrefix(slash, "../") {
		return ""
	}
	seg := strings.SplitN(slash, "/", 2)[0]
	if seg == "" || seg == "." || seg == ".." {
		return ""
	}
	return seg
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
