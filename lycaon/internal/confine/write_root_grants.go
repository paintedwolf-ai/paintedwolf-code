package confine

import (
	"os"
	"path/filepath"
	"slices"
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
	return CurrentControlPlane().Contains(fspath.CanonicalPath(p))
}

// ControlPlane is a snapshot of the control-plane layout for callers that ask
// about many paths at once.
type ControlPlane struct {
	// Tops are the state tree and each registered worker placement folder.
	Tops []string
	// Workspaces are the agent workspaces inside Tops, which are not control plane.
	Workspaces []string
}

// CurrentControlPlane resolves the layout from current host facts.
func CurrentControlPlane() ControlPlane {
	return ControlPlane{
		Tops:       controlPlaneReadDenyRootsResolved(),
		Workspaces: resolveEach(agentWorkspaceRootsUnderControlPlane()),
	}
}

// Contains reports whether canonical path p is control plane.
func (c ControlPlane) Contains(p string) bool {
	return !underAny(p, c.Workspaces) && underAny(p, c.Tops)
}

// Meets reports whether canonical path p is, contains or lies inside control plane.
func (c ControlPlane) Meets(p string) bool {
	if c.Contains(p) {
		return true
	}
	for _, top := range c.Tops {
		if PathAtOrUnder(top, p) {
			return true
		}
	}
	return false
}

// ControlPlanePathDenied is the one verdict the approval gate and the path
// resolver share, so a denied path neither mints a card nor resolves through a grant.
// sessionScratch is the invocation's own scratch root; like the confinement floor,
// the verdict leaves it to the invocation and still denies every other session's.
func ControlPlanePathDenied(path string, write bool, sessionScratch, sessionOutput string) bool {
	if write && writeRootContainsStateTree(fspath.CanonicalPath(path)) {
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
	if writeRootContainsStateTree(resolved) {
		return true, WriteRootCodeContainsState
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
	if writeRootContainsStateTree(resolved) {
		return true, WriteRootCodeContainsState
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

// PrivateTemp identifies the sandbox directories that present session scratch.
// An empty ScratchRoot gives each launch its own filesystem.
type PrivateTemp struct {
	Paths       []string
	ScratchRoot string
	Retained    []string
}

// Resolve maps a sandbox operand to its backing path for host file operations.
func (t PrivateTemp) Resolve(path string) (string, bool) {
	suffix, mapped := t.relative(path)
	if !mapped || t.ScratchRoot == "" {
		return "", false
	}
	if suffix == "" {
		return t.ScratchRoot, true
	}
	return t.ScratchRoot + string(filepath.Separator) + suffix, true
}

func (t PrivateTemp) relative(path string) (string, bool) {
	for _, alias := range t.Paths {
		if path == alias {
			return "", true
		}
		if suffix, inside := strings.CutPrefix(path, alias+string(filepath.Separator)); inside {
			return suffix, !underAny(path, t.Retained)
		}
	}
	return "", false
}

// ProjectPrivateTemp keeps attached, scratch, read-only and reviewed paths
// visible when their host paths are below a sandbox temp directory.
func ProjectPrivateTemp(projectID, scratch string, roots, granted, readRoots []string) PrivateTemp {
	temp := PrivateTempFor(fspath.CanonicalPath(scratch))
	if len(temp.Paths) == 0 {
		return temp
	}
	for _, list := range [][]string{roots, granted, grantedWriteRoots(projectID), readRoots, {temp.ScratchRoot}} {
		for _, path := range list {
			p := fspath.CanonicalPath(path)
			if slices.ContainsFunc(temp.Paths, func(alias string) bool { return PathStrictlyUnder(p, alias) }) {
				temp.Retained = append(temp.Retained, p)
			}
		}
	}
	slices.Sort(temp.Retained)
	temp.Retained = slices.Compact(temp.Retained)
	return temp
}
