package projectpaths

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
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
	abs := strings.TrimSpace(modelPath)
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

func approvedAccess(tctx tools.ToolContext, rootSession, abs string, write bool) (Access, bool) {
	accesses := append([]hitl.GrantedPathDelta(nil), tctx.Files.ApprovedFileAccess...)
	if tctx.Files.FileChangeReview != nil {
		accesses = append(accesses, tctx.Files.PreparedFileAccess...)
	}
	for _, access := range accesses {
		// The frozen canonical path prevents symlink retargeting from redirecting approval.
		if access.Path == abs && (!write || access.Write) {
			return Access{Path: abs}, true
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
