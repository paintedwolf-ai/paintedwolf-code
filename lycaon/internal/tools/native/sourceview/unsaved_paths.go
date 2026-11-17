package sourceview

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type unsavedPathsKey struct{}

// unsavedPaths remembers, per root, which paths held unsaved text when a
// program first read that root, so a walk over many files checks a map
// instead of asking the document store for each one.
type unsavedPaths struct {
	mu    sync.Mutex
	roots map[string]map[string]bool
}

// WithUnsavedPaths binds one program's view of which paths hold unsaved text.
func WithUnsavedPaths(ctx context.Context) context.Context {
	return context.WithValue(ctx, unsavedPathsKey{}, &unsavedPaths{roots: map[string]map[string]bool{}})
}

// HoldsUnsavedText reports whether the person has unsaved text open for this
// project-tree path. Without a program view it asks for the path alone.
func HoldsUnsavedText(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved) (bool, error) {
	documents, ok := DocumentsFor(tctx, resolved)
	if !ok {
		return false, nil
	}
	branch, err := tctx.SourceBranch(resolved.Root.ID)
	if err != nil {
		return false, err
	}
	memo, ok := ctx.Value(unsavedPathsKey{}).(*unsavedPaths)
	if !ok {
		found := false
		err := documents.VisitUnsavedDocuments(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, resolved.ScopeRel, false, func(tools.EditorDocumentState) error {
			found = true
			return nil
		})
		return found, err
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	paths, loaded := memo.roots[resolved.Root.ID]
	if !loaded {
		paths = map[string]bool{}
		err := documents.VisitUnsavedDocuments(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, "", true, func(state tools.EditorDocumentState) error {
			paths[state.Path] = true
			return nil
		})
		if err != nil {
			return false, err
		}
		memo.roots[resolved.Root.ID] = paths
	}
	return paths[resolved.ScopeRel], nil
}
