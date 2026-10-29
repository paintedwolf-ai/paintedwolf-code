package sourceview

import (
	"context"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// OpenDocument returns the accepted shared document for a project-tree path.
// A failed lookup is an error, never a silent fall back to disk.
func OpenDocument(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved) (tools.EditorDocumentText, bool, error) {
	documents, ok := DocumentsFor(tctx, resolved)
	if !ok {
		return tools.EditorDocumentText{}, false, nil
	}
	branch, err := tctx.SourceBranch(resolved.Root.ID)
	if err != nil {
		return tools.EditorDocumentText{}, false, err
	}
	doc, open, err := documents.OpenDocument(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, resolved.ScopeRel)
	if errors.Is(err, projectsource.ErrSourceIsDirectory) {
		return tools.EditorDocumentText{}, false, DirectoryReject(resolved.DisplayPath)
	}
	if err != nil {
		return tools.EditorDocumentText{}, false, fmt.Errorf("read collaborative document: %w", err)
	}
	return doc, open, nil
}
