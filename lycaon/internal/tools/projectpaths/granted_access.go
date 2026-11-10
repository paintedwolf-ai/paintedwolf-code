package projectpaths

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
)

// GrantedAccessSource resolves project-scoped path authority.
type GrantedAccessSource func(rootSessionID, projectID, abs string, write bool) (Access, bool)

// Access is the approved filesystem subject a grant installed.
type Access struct {
	Path string
	Tree bool
}

type accessRegistration struct {
	source GrantedAccessSource
}

var (
	grantedAccessMu sync.RWMutex
	grantedAccess   *accessRegistration
)

// SetGrantedAccessSource installs path authority and returns its owner's release.
func SetGrantedAccessSource(fn GrantedAccessSource) func() {
	registration := &accessRegistration{source: fn}
	grantedAccessMu.Lock()
	grantedAccess = registration
	grantedAccessMu.Unlock()
	return func() {
		grantedAccessMu.Lock()
		defer grantedAccessMu.Unlock()
		if grantedAccess == registration {
			grantedAccess = nil
		}
		registration.source = nil
	}
}

// resolveGranted anchors scope on the approved path.
func resolveGranted(tctx tools.ToolContext, modelPath string, op sandbox.PathOp) (Resolved, bool) {
	abs := modelPath
	if abs == "" || !filepath.IsAbs(abs) {
		// Grants apply only to absolute paths.
		return Resolved{}, false
	}
	// Canonical paths keep aliases within the granted tree.
	abs = filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(abs)))
	rootSession := strings.TrimSpace(tctx.Identity.ParentSessionID)
	if rootSession == "" {
		rootSession = strings.TrimSpace(tctx.Identity.SessionID)
	}
	access, ok := approvedAccess(tctx, rootSession, abs, op != sandbox.PathOpRead)
	if !ok {
		return Resolved{}, false
	}
	anchor := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(access.Path)))
	if !access.Tree {
		// Missing parents need an existing effect anchor; authority stays on the leaf.
		anchor = existingEffectAnchor(filepath.Dir(anchor))
	}
	scopeRel, err := filepath.Rel(anchor, abs)
	if err != nil || scopeRel == ".." || strings.HasPrefix(scopeRel, ".."+string(filepath.Separator)) {
		return Resolved{}, false
	}
	if scopeRel == "." {
		scopeRel = ""
	}
	return Resolved{
		Abs:         abs,
		DisplayPath: abs,
		ScopeRel:    filepath.ToSlash(scopeRel),
		Root:        projectroot.RootRef{Path: anchor},
		External:    true,
	}, true
}

func canonicalClean(path string) string {
	if path == "" || !filepath.IsAbs(path) {
		return ""
	}
	canon := fspath.CanonicalPath(path)
	if canon == "" {
		canon = path
	}
	return filepath.Clean(filepath.FromSlash(canon))
}

func effectiveRootSession(tctx tools.ToolContext) string {
	rootSession := strings.TrimSpace(tctx.Identity.ParentSessionID)
	if rootSession == "" {
		rootSession = strings.TrimSpace(tctx.Identity.SessionID)
	}
	return rootSession
}

// HasExactReadGrant reports whether tctx or the active GrantedAccessSource
// holds an exact, non-tree, read-capable approval for the file at absPath.
// Directories and control-plane state are never eligible.
func HasExactReadGrant(tctx tools.ToolContext, absPath string) bool {
	abs := canonicalClean(absPath)
	if abs == "" {
		return false
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return false
	}
	if confine.ControlPlanePathDenied(absPath, false, tctx.Host.SessionScratchDir, tctx.Host.SessionOutputDir) ||
		confine.ControlPlanePathDenied(abs, false, tctx.Host.SessionScratchDir, tctx.Host.SessionOutputDir) ||
		confine.ControlPlaneTree(abs) {
		return false
	}
	for _, access := range tctx.Files.ApprovedFileAccess {
		if !access.Tree && !access.Write && canonicalClean(access.Path) == abs {
			return true
		}
	}
	if tctx.Files.FileChangeReview != nil {
		for _, access := range tctx.Files.PreparedFileAccess {
			if !access.Tree && !access.Write && canonicalClean(access.Path) == abs {
				return true
			}
		}
	}
	for _, p := range tctx.Files.SessionReadPaths {
		if canonicalClean(p) == abs {
			return true
		}
	}
	grantedAccessMu.RLock()
	var source GrantedAccessSource
	if grantedAccess != nil {
		source = grantedAccess.source
	}
	grantedAccessMu.RUnlock()
	if source != nil {
		if access, ok := source(effectiveRootSession(tctx), tctx.Identity.ProjectID, abs, false); ok {
			if !access.Tree && canonicalClean(access.Path) == abs {
				return true
			}
		}
	}
	return false
}

func approvedAccess(tctx tools.ToolContext, rootSession, abs string, write bool) (Access, bool) {
	abs = canonicalClean(abs)
	if abs == "" {
		return Access{}, false
	}
	if !write && confine.IsAgentSourceReadDenied("", abs, tctx.Host.SessionScratchDir, tctx.Host.SessionOutputDir) {
		if !HasExactReadGrant(tctx, abs) {
			return Access{}, false
		}
		return Access{Path: abs, Tree: false}, true
	}
	for _, access := range tctx.Files.ApprovedFileAccess {
		// The frozen canonical path prevents symlink retargeting from redirecting approval.
		if !access.Tree && canonicalClean(access.Path) == abs && access.Write == write {
			return Access{Path: abs}, true
		}
	}
	if tctx.Files.FileChangeReview != nil {
		for _, access := range tctx.Files.PreparedFileAccess {
			if !access.Tree && canonicalClean(access.Path) == abs && (!write || access.Write) {
				return Access{Path: abs}, true
			}
		}
	}
	if !write {
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			for _, p := range tctx.Files.SessionReadPaths {
				if canonicalClean(p) == abs {
					return Access{Path: abs}, true
				}
			}
		}
	}
	grantedAccessMu.RLock()
	var source GrantedAccessSource
	if grantedAccess != nil {
		source = grantedAccess.source
	}
	grantedAccessMu.RUnlock()
	if source != nil {
		return source(rootSession, tctx.Identity.ProjectID, abs, write)
	}
	return Access{}, false
}
