package native

import (
	"context"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// CheckUnsavedDraft refuses writes at an unsaved document or destructive
// operations on a subtree containing one. The metadata lookup never opens text.
func (e ExecutionRevisions) CheckUnsavedDraft(ctx context.Context, tctx tools.ToolContext, path string, descendants bool) error {
	resolved, err := projectpaths.ResolveWrite(ctx, e.Boundary, tctx, path)
	if err != nil {
		return err
	}
	documents, ok := sourceview.DocumentsFor(tctx, resolved)
	if !ok {
		return nil
	}
	branch, err := tctx.SourceBranch(resolved.Root.ID)
	if err != nil {
		return err
	}
	queryPath := resolved.ScopeRel
	if queryPath == "." {
		queryPath = ""
	}
	return documents.VisitUnsavedDocuments(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, queryPath, descendants, func(state tools.EditorDocumentState) error {
		resolved.ScopeRel, resolved.DisplayPath = state.Path, state.Path
		return unsavedDraftConflict(resolved, state)
	})
}
