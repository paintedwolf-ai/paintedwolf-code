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
	documents, address, ok := sourceview.DocumentsFor(tctx, resolved)
	if !ok {
		return nil
	}
	branch, err := tctx.SourceBranch(address.RootID)
	if err != nil {
		return err
	}
	queryPath := address.Path
	if queryPath == "." {
		queryPath = ""
	}
	return documents.VisitUnsavedDocuments(ctx, tctx.Identity.ProjectID, branch, address.RootID, queryPath, descendants, func(state tools.EditorDocumentState) error {
		resolved.ScopeRel, resolved.DisplayPath = state.Path, address.DisplayPath(state.Path)
		return unsavedDraftConflict(resolved, state)
	})
}
