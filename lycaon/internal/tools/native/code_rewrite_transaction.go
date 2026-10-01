package native

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type rewritePlan struct {
	resolved projectpaths.Resolved
	source   sourceview.Text
	content  string
}

// Open documents accept the complete edit set before any file is published.
// Closed files retain the filesystem write door and its individual CAS outcome.
func landRewritePlans(ctx context.Context, tctx tools.ToolContext, plans []rewritePlan, out *multiApplyOut) error {
	var edits []tools.EditorDocumentEdit
	var reviews []tools.FileChange
	var indices []int
	for i, plan := range plans {
		if plan.source.Editor == nil {
			continue
		}
		edits = append(edits, tools.EditorDocumentEdit{DocumentID: plan.source.Editor.ID,
			ExpectedRevision: plan.source.Editor.Revision, Content: plan.content, OperationID: uuid.NewString(),
			SessionID: tctx.SessionID, Turn: tctx.UserTurn, ToolCallID: tctx.ToolCallID, ToolName: "code_rewrite"})
		indices = append(indices, i)
	}
	if len(edits) > 0 {
		documents, ok := sourceview.ProjectDocuments(tctx)
		if !ok {
			return fmt.Errorf("collaborative documents unavailable for rewrite transaction")
		}
		previews, err := documents.PreviewAgentEdits(ctx, tctx.ProjectID, edits)
		if err != nil {
			return err
		}
		for i, preview := range previews {
			edits[i].ReviewedRevision = preview.Before.Revision
			reviews = append(reviews, editorFileChange(tctx, plans[indices[i]].resolved, preview))
		}
		if err := tctx.ReviewFileChanges(ctx, reviews...); err != nil {
			return err
		}
		results, err := documents.ApplyAgentEdits(ctx, tctx.ProjectID, edits)
		if err != nil {
			return err
		}
		for i, result := range results {
			documents.AdvanceAgentRead(tctx.ProjectID, tctx.SessionID, tctx.EditorReadBases, edits[i], result.Document)
			reportLanded(tctx, result.Document)
			plan := plans[indices[i]]
			tctx.RecordModelAuthoredCredentials(ctx, plan.resolved.Abs, []byte(plan.content))
			reportLandedEditorConfig(ctx, tctx, plan.resolved, plan.source, plan.content)
			row := &out.Changed[indices[i]]
			row.InEditor, row.Unsaved = true, !result.Saved
			if result.PublicationError != nil {
				row.SaveError = result.PublicationError.Error()
			}
		}
	}
	accepted := make([]multiApplyFileOut, 0, len(plans))
	for i, plan := range plans {
		if plan.source.Editor == nil {
			if _, err := landEditedText(ctx, tctx, "code_rewrite", plan.resolved, plan.source, plan.content); err != nil {
				out.Failed = append(out.Failed, rewriteFailure{Path: plan.resolved.DisplayPath, Error: err.Error()})
				continue
			}
		}
		before := plan.source.Content
		captureFileEdit(tctx, plan.resolved.DisplayPath, plan.content, &before)
		afterSuccessfulMutation(ctx, tctx, plan.resolved.DisplayPath)
		accepted = append(accepted, out.Changed[i])
	}
	out.Changed = accepted
	return nil
}
