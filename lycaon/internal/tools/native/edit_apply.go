package native

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// applyEditedContent applies gates, lands, and records one edit.
func (t *EditTool) applyEditedContent(ctx context.Context, path string, resolved projectpaths.Resolved, st sourceview.Text, before, newContent string, tctx tools.ToolContext, seamStartLine, seamLinesAdded int) (string, landing, error) {
	if err := guardMutationContent("edit", path, newContent); err != nil {
		return "", landing{}, err
	}
	reportTextIntent(tctx, resolved, st, true, newContent)
	if t.ContentApply != nil {
		beforeCopy := before
		gated, gateErr := t.ContentApply.GateApply(ctx, "edit", path, &beforeCopy, newContent, tctx)
		if gateErr != nil {
			return "", landing{}, gateErr
		}
		if gated != newContent {
			reportTextIntent(tctx, resolved, st, true, gated)
		}
		newContent = gated
	}
	if err := rejectIfSyntaxUnhealthy(ctx, "edit", path, &before, newContent, mutationSeam{
		FinalStartLine: seamStartLine, FinalEndLine: seamStartLine + max(seamLinesAdded-1, 0),
	}); err != nil {
		return "", landing{}, err
	}
	landed, err := landEditedText(ctx, tctx, "edit", resolved, st, newContent)
	if err != nil {
		return "", landing{}, err
	}
	beforeCopy := before
	captureFileEdit(tctx, path, newContent, &beforeCopy)
	afterSuccessfulMutation(ctx, tctx, path)
	return newContent, landed, nil
}

// finishEdit applies content and renders the edit receipt.
func (t *EditTool) finishEdit(ctx context.Context, path string, resolved projectpaths.Resolved, st sourceview.Text, before, newContent string, tctx tools.ToolContext, replaced, oldLen, newLen, seamStartLine, seamLinesAdded int) (string, error) {
	final, landed, err := t.applyEditedContent(ctx, path, resolved, st, before, newContent, tctx, seamStartLine, seamLinesAdded)
	if err != nil {
		return "", err
	}
	receipt := fmt.Sprintf("Edited %s: replaced %d occurrence(s), %d chars with %d chars", path, replaced, oldLen, newLen)
	if seamStartLine > 0 {
		if seams := seamContext(final, seamStartLine, seamLinesAdded); seams != "" {
			receipt += "\n" + seams
		}
	}
	if note := landed.note(); note != "" {
		receipt += "\n" + note
	}
	return receipt, nil
}
