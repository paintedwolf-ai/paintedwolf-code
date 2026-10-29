package native

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// applyPinned publishes a program's prepared bytes at the destination and
// source revisions it captured. It needs no conversational read and never
// advances one; a moved revision is a conflict and nothing is retried.
func (t *WriteTool) applyPinned(ctx context.Context, input WriteInput, tctx tools.ToolContext, resolved projectpaths.Resolved) (WriteResult, error) {
	if !capturedPin(input.Destination, toolcontract.DestinationRevision) || !sameLocation(resolved, input.Destination) {
		return WriteResult{}, unsupportedWriteAdmission()
	}
	for _, source := range input.Sources {
		if !capturedPin(source, toolcontract.SourceRevision) {
			return WriteResult{}, unsupportedWriteAdmission()
		}
	}
	revisions := ExecutionRevisions{Boundary: t.Boundary}
	st, current, err := captureRevision(ctx, tctx, resolved, toolcontract.DestinationRevision)
	if err != nil {
		return WriteResult{}, err
	}
	if !current.Same(input.Destination) {
		return WriteResult{}, &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: input.Destination, Current: current}
	}
	if err := revisions.Verify(ctx, tctx, input.Sources...); err != nil {
		return WriteResult{}, err
	}
	var before *string
	if current.Mode != toolcontract.AbsentRevision {
		text := st.Content
		before = &text
	}
	return t.writeText(ctx, tctx, resolved, st, before, input, func(final string) (landing, error) {
		landed, err := landPinnedText(ctx, tctx, resolved, st, input, final, func() error { return revisions.Verify(ctx, tctx, input.Sources...) })
		if conflictsWithPin(err) {
			return landing{}, &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: input.Destination, Cause: err}
		}
		return landed, err
	})
}

func capturedPin(pin toolcontract.ExecutionRevision, role toolcontract.ExecutionRevisionRole) bool {
	return pin.Validate() == nil && pin.Role == role && pin.Basis == toolcontract.CapturedRevision
}

// conflictsWithPin recognizes the owners' typed refusals of a moved base.
func conflictsWithPin(err error) bool {
	var conflict *ExecutionRevisionConflict
	if err == nil || errors.As(err, &conflict) {
		return false
	}
	if errors.Is(err, tools.ErrEditorExecutionConflict) || errors.Is(err, errMutationSnapshotMoved) {
		return true
	}
	reject := toolrejection.AsToolReject(err)
	return reject != nil && reject.Code == "TEXT_WRITE_CONFLICT"
}

// landPinnedText lands through the same document owner or write door as a
// conversational write. admitSources runs after review, immediately before
// acceptance, so inputs cannot silently refresh while approval waits.
func landPinnedText(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, st sourceview.Text, input WriteInput, newContent string, admitSources func() error) (landing, error) {
	if err := guardMutationContent("write", resolved.DisplayPath, newContent); err != nil {
		return landing{}, err
	}
	if st.Editor == nil {
		after, err := sourceview.EncodeText("write", resolved.DisplayPath, st.Disk, newContent)
		if err != nil {
			return landing{}, err
		}
		var beforeRaw []byte
		baseSHA := ""
		if st.Disk != nil {
			beforeRaw, baseSHA = st.Disk.RawBytes(), st.Disk.RawSHA256()
		}
		if err := applyAgentFileAdmitted(ctx, tctx, resolvedMutationTarget(resolved), after, beforeRaw, baseSHA, admitSources); err != nil {
			return landing{}, err
		}
		reportLandedEditorConfig(ctx, tctx, resolved, st, newContent)
		return landing{change: mutationContext(st.Content, newContent), publishedSHA256: textfile.SHA256(after)}, nil
	}
	documents, ok := sourceview.DocumentsFor(tctx, resolved)
	if !ok {
		return landing{}, &toolrejection.ToolReject{Code: "EDITOR_DOCUMENT_CHANGING", Data: map[string]any{"path": resolved.DisplayPath}}
	}
	pin := input.Destination
	edit := tools.EditorDocumentEdit{DocumentID: st.Editor.ID, ExpectedRevision: pin.DocumentRevision, Content: newContent,
		OperationID: uuid.NewString(), SessionID: tctx.Identity.SessionID, Turn: tctx.Identity.UserTurn, ToolCallID: tctx.Identity.ToolCallID, ToolName: "write",
		Pin: &tools.EditorDocumentPin{Revision: pin.DocumentRevision, BaseSHA256: pin.DiskSHA256, Absent: pin.DiskSHA256 == ""}}
	previews, err := documents.PreviewAgentEdits(ctx, tctx.Identity.ProjectID, []tools.EditorDocumentEdit{edit})
	if err != nil {
		return landing{}, err
	}
	preview := previews[0]
	if err := tctx.ReviewFileChanges(ctx, editorFileChange(tctx, resolved, preview)); err != nil {
		return landing{}, err
	}
	if err := admitSources(); err != nil {
		return landing{}, err
	}
	edit.ReviewedRevision = pin.DocumentRevision
	applied, err := documents.ApplyAgentEdit(ctx, tctx.Identity.ProjectID, edit)
	if err != nil {
		return landing{}, err
	}
	tctx.RecordModelAuthoredCredentials(ctx, resolved.Abs, []byte(newContent))
	reportLanded(tctx, applied.Document)
	reportLandedEditorConfig(ctx, tctx, resolved, st, newContent)
	landed := landing{change: mutationContext(preview.Before.Text, applied.Document.Text), editor: true, saved: applied.Saved,
		recreated: st.Editor.Absent && applied.Saved, heldVersionID: applied.HeldVersionID, publicationError: applied.PublicationError,
		documentID: applied.Document.ID, acceptedRevision: pin.DocumentRevision + 1}
	if applied.Saved {
		landed.publishedSHA256 = applied.Document.BaseSHA256
	}
	return landed, nil
}
