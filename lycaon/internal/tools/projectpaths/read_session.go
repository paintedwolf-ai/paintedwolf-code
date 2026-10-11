package projectpaths

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/internal/tools"
)

// ReadSession holds admitted roots open for descriptor-relative reads.
// Paths outside those roots use ResolveRead.
type ReadSession struct {
	boundary *sandbox.Boundary
	tctx     tools.ToolContext
	// admitted reports that the call's attached roots passed validation.
	admitted bool

	mu     sync.Mutex
	roots  map[string]*fseffect.ReadRoot
	closed bool
}

// NewReadSession admits tctx's attached roots for the reads of one call.
func NewReadSession(b *sandbox.Boundary, tctx tools.ToolContext) *ReadSession {
	admitted := strings.TrimSpace(tctx.Source.WorkerBranchRoot) == "" && len(tctx.Source.Roots) > 0 &&
		tools.ValidateAttachedRootsForAction(actionRootPaths(tctx)) == nil
	return &ReadSession{boundary: b, tctx: tctx, admitted: admitted, roots: map[string]*fseffect.ReadRoot{}}
}

// Resolve applies ResolveRead's rules to modelPath.
func (s *ReadSession) Resolve(ctx context.Context, modelPath string) (Resolved, error) {
	if resolved, ok, err := s.resolveBeneathRoot(ctx, modelPath); ok {
		return resolved, err
	}
	if s.admitted {
		return resolveUnderAdmittedRoots(ctx, s.boundary, s.tctx, modelPath, sandbox.PathOpRead)
	}
	return ResolveRead(ctx, s.boundary, s.tctx, modelPath)
}

// resolveBeneathRoot places an ordinary path beneath an admitted attached
// root; ok is false for every other path.
func (s *ReadSession) resolveBeneathRoot(ctx context.Context, modelPath string) (Resolved, bool, error) {
	if !s.admitted || s.hostAddressed(modelPath) {
		return Resolved{}, false, nil
	}
	abs, root, primary, placed := s.placeBeneathRoot(modelPath)
	if !placed {
		return Resolved{}, false, nil
	}
	scopeRel := projectroot.ScopeRel(root, abs)
	if s.boundary != nil {
		if err := s.boundary.AssertReadGlobs(ctx, scopeRel, s.tctx.ProfileID()); err != nil {
			return Resolved{}, true, err
		}
	}
	return Resolved{
		Abs:         abs,
		DisplayPath: projectroot.Qualify(primary, root, abs),
		ScopeRel:    scopeRel,
		Root:        root,
	}, true, nil
}

// placeBeneathRoot resolves modelPath against the attached roots. A path the
// roots cannot place is left to the general resolver, which reports why.
func (s *ReadSession) placeBeneathRoot(modelPath string) (abs string, root, primary projectroot.RootRef, ok bool) {
	abs, root, err := projectroot.ResolveAbs(s.tctx.Source.Roots, s.tctx.Source.ActiveRootID, modelPath)
	if err != nil {
		return "", projectroot.RootRef{}, projectroot.RootRef{}, false
	}
	primary, err = projectroot.PrimaryRoot(s.tctx.Source.Roots)
	if err != nil {
		return "", projectroot.RootRef{}, projectroot.RootRef{}, false
	}
	return abs, root, primary, true
}

// hostAddressed reports a path the host's own namespaces answer before any
// attached root: agent spill output and the session scratch folder.
func (s *ReadSession) hostAddressed(modelPath string) bool {
	if host := strings.TrimSpace(s.tctx.Host.HostDataDir); host != "" {
		if _, ok := tooloutput.AgentWireSpillScopeRel(host, modelPath); ok {
			return true
		}
	}
	if _, ok := projectroot.ScratchAddress(modelPath); ok {
		return true
	}
	scratch := filepath.Clean(strings.TrimSpace(s.tctx.Host.SessionScratchDir))
	path := filepath.Clean(strings.TrimSpace(modelPath))
	return scratch != "." && filepath.IsAbs(path) && (path == scratch || strings.HasPrefix(path, scratch+string(filepath.Separator)))
}

// Open opens a path Resolve returned relative to its root's held descriptor,
// exactly as fseffect.OpenRead would open it.
func (s *ReadSession) Open(resolved Resolved) (*os.File, error) {
	loc := resolved.EffectLocation()
	root, err := s.heldRoot(loc.Root)
	if err != nil {
		return nil, err
	}
	return root.Open(loc.Rel)
}

func (s *ReadSession) heldRoot(path string) (*fseffect.ReadRoot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if root := s.roots[path]; root != nil {
		return root, nil
	}
	root, err := fseffect.OpenReadRoot(path)
	if err != nil {
		return nil, err
	}
	s.roots[path] = root
	return root, nil
}

// Close releases the held roots. Files already opened stay readable.
func (s *ReadSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for path, root := range s.roots {
		_ = root.Close()
		delete(s.roots, path)
	}
}
