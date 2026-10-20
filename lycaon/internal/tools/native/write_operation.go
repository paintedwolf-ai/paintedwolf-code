package native

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"os"
)

// WriteInput is shared by retained structured write and native command adapters.
type WriteInput struct {
	Path    string
	Content string
	Append  bool
}

// WriteResult distinguishes accepted document edits from saved publication.
type WriteResult struct {
	Path             string
	Appended         bool
	InputBytes       int
	ResultBytes      int
	Accepted         bool
	Saved            bool
	HeldVersionID    string
	PublicationError error
	Note             string
}

// Apply performs the complete native write operation, including scope, review,
// revision, syntax, attribution and the existing source write door.
func (t *WriteTool) Apply(ctx context.Context, input WriteInput, tctx tools.ToolContext) (WriteResult, error) {
	path, content, appendMode := input.Path, input.Content, input.Append
	if path == "" {
		return WriteResult{}, toolkit.MissingArg("path")
	}
	if err := assertProfileWriteScope(ctx, t.Boundary, tctx, path, "write"); err != nil {
		return WriteResult{}, writeScopeReject(ctx, t.Boundary, path, tctx.ProfileID(), "write", err)
	}
	if err := beforeWorkerMutation(ctx, tctx, path); err != nil {
		return WriteResult{}, err
	}
	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, path)
	if err != nil {
		return WriteResult{}, err
	}
	fullPath := resolved.Abs
	path = resolved.DisplayPath
	return retryEditorOperation(ctx, path, func() (WriteResult, error) {
		// A missing file is a create; otherwise the base is the served source text.
		var before *string
		st, err := loadAgentSourceText(ctx, "write", tctx, resolved)
		if err == nil {
			beforeText := st.Content
			before = &beforeText
		} else if !os.IsNotExist(err) {
			return WriteResult{}, fmt.Errorf("read failed: %w", err)
		}
		proposed := content
		if appendMode {
			if before == nil {
				return WriteResult{}, sourceview.PathNotFound("write", path, fullPath)
			}
			proposed = *before + content
		}
		if err := guardMutationContent("write", path, proposed); err != nil {
			return WriteResult{}, err
		}
		finalContent := proposed
		reportTextIntent(tctx, resolved, st, before != nil, proposed)
		if t.ContentApply != nil {
			var err error
			finalContent, err = t.ContentApply.GateApply(ctx, "write", path, before, proposed, tctx)
			if err != nil {
				return WriteResult{}, err
			}
			if finalContent != proposed {
				reportTextIntent(tctx, resolved, st, before != nil, finalContent)
			}
		}
		if err := rejectIfSyntaxUnhealthy(ctx, "write", path, before, finalContent, mutationSeam{}); err != nil {
			return WriteResult{}, err
		}
		landed, err := landEditedText(ctx, tctx, "write", resolved, st, finalContent)
		if err != nil {
			return WriteResult{}, err
		}
		captureFileEdit(tctx, path, finalContent, before)
		afterSuccessfulMutation(ctx, tctx, path)
		return WriteResult{Path: path, Appended: appendMode, InputBytes: len(content), ResultBytes: len(finalContent),
			Accepted: true, Saved: !landed.editor || landed.saved, HeldVersionID: landed.heldVersionID,
			PublicationError: landed.publicationError, Note: landed.note()}, nil
	})
}

func (result WriteResult) Receipt() string {
	var receipt string
	if result.Appended {
		receipt = fmt.Sprintf("Appended %d bytes to %s (file now %d bytes)", result.InputBytes, result.Path, result.ResultBytes)
	} else {
		receipt = fmt.Sprintf("Wrote %d bytes to %s", result.ResultBytes, result.Path)
	}
	if result.Note != "" {
		receipt += "\n" + result.Note
	}
	return receipt
}
