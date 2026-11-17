package sourceview

import (
	"context"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// HoldsUnsavedText reports whether the person has unsaved text open for this
// project-tree path. Inside a program it consults the program's view;
// otherwise it asks for the path alone.
func HoldsUnsavedText(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved) (bool, error) {
	documents, ok := DocumentsFor(tctx, resolved)
	if !ok {
		return false, nil
	}
	branch, err := tctx.SourceBranch(resolved.Root.ID)
	if err != nil {
		return false, err
	}
	visit := func(path string, descendants bool, seen func(tools.EditorDocumentState)) error {
		return documents.VisitUnsavedDocuments(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, path, descendants, func(state tools.EditorDocumentState) error {
			seen(state)
			return nil
		})
	}
	memo, ok := tools.ProgramUnsavedPaths(ctx)
	if !ok {
		found := false
		err := visit(resolved.ScopeRel, false, func(tools.EditorDocumentState) { found = true })
		return found, err
	}
	return memo.Holds(resolved.Root.ID, resolved.ScopeRel, func() (map[string]bool, error) {
		paths := map[string]bool{}
		err := visit("", true, func(state tools.EditorDocumentState) { paths[state.Path] = true })
		return paths, err
	})
}
