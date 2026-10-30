package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
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

// applyPinnedBytes publishes exact program output at the captured destination.
// An open document still owns its path and receives the bytes as text; other
// destinations land byte for byte through the reviewed, recoverable stream
// door. Text output keeps the write gates (content review, syntax health);
// binary output is reviewed by size and hash, never by its raw bytes.
func (t *WriteTool) applyPinnedBytes(ctx context.Context, input WriteInput, tctx tools.ToolContext, resolved projectpaths.Resolved) (WriteResult, error) {
	if !capturedPin(input.Destination, toolcontract.DestinationRevision) || !sameLocation(resolved, input.Destination) {
		return WriteResult{}, unsupportedWriteAdmission()
	}
	for _, source := range input.Sources {
		if !capturedPin(source, toolcontract.SourceRevision) {
			return WriteResult{}, unsupportedWriteAdmission()
		}
	}
	revisions := ExecutionRevisions{Boundary: t.Boundary}
	st, current, raw, err := captureDestinationBytes(ctx, tctx, resolved)
	if err != nil {
		return WriteResult{}, err
	}
	if !current.Same(input.Destination) {
		return WriteResult{}, &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: input.Destination, Current: current}
	}
	if err := revisions.Verify(ctx, tctx, input.Sources...); err != nil {
		return WriteResult{}, err
	}
	admitSources := func() error { return revisions.Verify(ctx, tctx, input.Sources...) }
	exists := current.Mode != toolcontract.AbsentRevision
	if st.Editor != nil {
		if !utf8.Valid(input.Bytes) {
			return WriteResult{}, &toolrejection.ToolReject{Code: "WRITE_BINARY_DENIED", Data: map[string]any{
				"path": resolved.DisplayPath, "bytes": len(input.Bytes), "reason": "binary output into an open text document", "max_file_bytes": readcaps.MaxMutationBytes,
			}}
		}
		content := string(input.Bytes)
		if input.Append && exists {
			content = st.Content + content
		}
		before := st.Content
		return t.writeText(ctx, tctx, resolved, st, &before, WriteInput{Path: input.Path, Content: content, Destination: input.Destination}, func(final string) (landing, error) {
			landed, err := landPinnedText(ctx, tctx, resolved, st, input, final, admitSources)
			if conflictsWithPin(err) {
				return landing{}, &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: input.Destination, Cause: err}
			}
			return landed, err
		})
	}
	proposed := input.Bytes
	if input.Append && exists {
		proposed = append(append([]byte(nil), raw...), input.Bytes...)
	}
	land := func(final []byte) (landing, error) {
		landed, err := landPinnedBytes(ctx, tctx, resolved, input.Destination, final, admitSources)
		if conflictsWithPin(err) {
			return landing{}, &ExecutionRevisionConflict{Path: resolved.DisplayPath, Pinned: input.Destination, Cause: err}
		}
		return landed, err
	}
	// Bytes the text door accepts keep its gates; everything else is binary.
	if utf8.Valid(proposed) && guardMutationContent("write", resolved.DisplayPath, string(proposed)) == nil {
		var before *string
		if exists && st.Disk != nil {
			text := st.Content
			before = &text
		}
		return t.writeText(ctx, tctx, resolved, st, before, WriteInput{Path: input.Path, Content: string(proposed), Destination: input.Destination}, func(final string) (landing, error) {
			return land([]byte(final))
		})
	}
	landed, err := land(proposed)
	if err != nil {
		return WriteResult{}, err
	}
	afterSuccessfulMutation(ctx, tctx, resolved.DisplayPath)
	return WriteResult{Path: resolved.DisplayPath, Appended: input.Append, InputBytes: len(input.Bytes), ResultBytes: len(proposed),
		Accepted: true, Saved: true, PublishedSHA256: landed.publishedSHA256}, nil
}

// landPinnedBytes stages final through the agent stream door, which reviews,
// journals and keeps any replaced file recoverable. Immediately before commit
// the destination must still hold the captured bytes (or still be absent),
// and admitSources rechecks the inputs the bytes came from.
func landPinnedBytes(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, pin toolcontract.ExecutionRevision, final []byte, admitSources func() error) (landing, error) {
	result, err := applyAgentStream(ctx, tctx, agentStreamRequest{Target: resolvedMutationTarget(resolved), Source: bytes.NewReader(final),
		BeforeCommit: func(current fseffect.Target, _ fseffect.Result) error {
			if err := verifyRawBase(current, resolved.DisplayPath, pin.DiskSHA256); err != nil {
				return err
			}
			return admitSources()
		}})
	if err != nil {
		return landing{}, err
	}
	return landing{publishedSHA256: result.SHA256}, nil
}

// verifyRawBase compares the held destination with the captured file bytes,
// whatever their size or encoding; an empty base requires absence.
func verifyRawBase(target fseffect.Target, path, baseSHA256 string) error {
	conflict := &toolrejection.ToolReject{Code: "TEXT_WRITE_CONFLICT", Data: map[string]any{"path": path, "text_base_changed": true}}
	if baseSHA256 == "" {
		if _, err := target.Lstat(); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		return conflict
	}
	f, err := target.Open()
	if err != nil {
		return conflict
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return conflict
	}
	if hex.EncodeToString(h.Sum(nil)) != baseSHA256 {
		return conflict
	}
	return nil
}
