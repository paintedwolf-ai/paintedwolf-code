package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// Retry when a client draft advances between the tool's read and write.
const editorDocumentRetries = 3

// Mutations start from the text delivered to the agent, within the mutation
// budget so every replaced state stays retained and restorable.
func loadAgentSourceText(ctx context.Context, tool string, tctx tools.ToolContext, resolved projectpaths.Resolved) (sourceview.Text, error) {
	st, err := sourceview.LoadText(ctx, tool, sourceview.AccessMutate, tctx, resolved)
	if err != nil || st.Editor == nil {
		return st, err
	}
	documents, ok := sourceview.DocumentsFor(tctx, resolved)
	if !ok {
		return sourceview.Text{}, &toolrejection.ToolReject{Code: "EDITOR_DOCUMENT_CHANGING", Data: map[string]any{"path": resolved.DisplayPath}}
	}
	base, err := documents.AgentReadBase(ctx, tctx.Identity.ProjectID, tctx.Identity.SessionID, st.Editor.ID, tctx.Source.EditorReadBases)
	if errors.Is(err, tools.ErrEditorReadRequired) {
		return sourceview.Text{}, &toolrejection.ToolReject{Code: "EDITOR_DOCUMENT_READ_REQUIRED", Data: map[string]any{"path": resolved.DisplayPath}}
	}
	if err != nil && base.ID == "" {
		return sourceview.Text{}, err
	}
	return sourceview.Text{Content: base.Text, Editor: &base}, nil
}

// landing reports where an edit's bytes ended up.
type landing struct {
	// editor is true when the edit landed in the open document.
	editor bool
	// saved is true when the file on disk holds the edit.
	saved bool
	// recreated is true when the save brought back a file that had been deleted.
	recreated bool
	// heldVersionID addresses the retained state holding an unsaved edit.
	heldVersionID string
	// publicationError is why the file could not be written; nil when the
	// edit is withheld for a diverged file.
	publicationError error
}

// note is the receipt line stating the landing when it was not a plain file
// write. An unsaved landing says the disk still holds other bytes.
func (l landing) note() string {
	switch {
	case !l.editor:
		return ""
	case l.saved && l.recreated:
		return "Applied in the shared editor document and saved; the file had been deleted on disk and this save recreated it."
	case l.saved:
		return "Applied in the shared editor document and saved."
	}
	note := "Applied in the shared editor document, where the person sees it; not saved to disk"
	if l.publicationError != nil {
		note += ": publication failed (" + l.publicationError.Error() + ")"
	} else {
		note += ": the file changed outside the editor, so the edit waits for the person's merge"
	}
	note += ". The file on disk still holds its earlier content, which is what commands and tests read until the document is saved."
	if l.heldVersionID != "" {
		note += " The edit is retained as version " + l.heldVersionID + "."
	}
	return note
}

// landEditedText updates the source document or file. It is the final
// content guard: review gates can change the text a tool proposed.
// A changed document returns ErrEditorDocumentMoved before writing.
func landEditedText(ctx context.Context, tctx tools.ToolContext, tool string, resolved projectpaths.Resolved, st sourceview.Text, newContent string) (landing, error) {
	if err := guardMutationContent(tool, resolved.DisplayPath, newContent); err != nil {
		return landing{}, err
	}
	if st.Editor == nil {
		after, err := sourceview.EncodeText(tool, resolved.DisplayPath, st.Disk, newContent)
		if err != nil {
			return landing{}, err
		}
		var beforeRaw []byte
		baseSHA := ""
		if st.Disk != nil {
			beforeRaw = st.Disk.RawBytes()
			baseSHA = st.Disk.RawSHA256()
		}
		if err := applyAgentFile(ctx, tctx, resolvedMutationTarget(resolved), after, beforeRaw, baseSHA); err != nil {
			return landing{}, err
		}
		reportLandedEditorConfig(ctx, tctx, resolved, st, newContent)
		if documents, ok := sourceview.DocumentsFor(tctx, resolved); ok {
			if branch, berr := tctx.SourceBranch(resolved.Root.ID); berr == nil {
				// The written text is the agent's own; this response can edit from it.
				if doc, ok, err := documents.OpenDocument(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, resolved.ScopeRel); err == nil && ok {
					documents.AdvanceAgentRead(tctx.Identity.ProjectID, tctx.Identity.SessionID, tctx.Source.EditorReadBases,
						tools.EditorDocumentEdit{DocumentID: doc.ID, Content: newContent}, doc)
				}
			}
		}
		return landing{}, nil
	}
	documents, ok := sourceview.DocumentsFor(tctx, resolved)
	if !ok {
		return landing{}, &toolrejection.ToolReject{Code: "EDITOR_DOCUMENT_CHANGING", Data: map[string]any{"path": resolved.DisplayPath}}
	}

	edit := tools.EditorDocumentEdit{DocumentID: st.Editor.ID, ExpectedRevision: st.Editor.Revision, Content: newContent,
		OperationID: uuid.NewString(), SessionID: tctx.Identity.SessionID, Turn: tctx.Identity.UserTurn, ToolCallID: tctx.Identity.ToolCallID, ToolName: tool}
	previews, err := documents.PreviewAgentEdits(ctx, tctx.Identity.ProjectID, []tools.EditorDocumentEdit{edit})
	if err != nil {
		return landing{}, err
	}
	preview := previews[0]
	change := editorFileChange(tctx, resolved, preview)
	if err := tctx.ReviewFileChanges(ctx, change); err != nil {
		return landing{}, err
	}
	edit.ReviewedRevision = preview.Before.Revision
	applied, err := documents.ApplyAgentEdit(ctx, tctx.Identity.ProjectID, edit)
	if err != nil {
		return landing{}, err
	}
	documents.AdvanceAgentRead(tctx.Identity.ProjectID, tctx.Identity.SessionID, tctx.Source.EditorReadBases, edit, applied.Document)
	tctx.RecordModelAuthoredCredentials(ctx, resolved.Abs, []byte(newContent))
	reportLanded(tctx, applied.Document)
	reportLandedEditorConfig(ctx, tctx, resolved, st, newContent)
	return landing{editor: true, saved: applied.Saved, recreated: st.Editor.Absent && applied.Saved,
		heldVersionID: applied.HeldVersionID, publicationError: applied.PublicationError}, nil
}

// Retrying can refresh a prepared review while retaining the same agent read.
// Persistent anchor conflicts return to the agent for another read.
func retryEditorDocument(ctx context.Context, path string, body func() (string, error)) (string, error) {
	var out string
	var err error
	for attempt := 0; attempt < editorDocumentRetries; attempt++ {
		out, err = body()
		if !errors.Is(err, tools.ErrEditorDocumentMoved) {
			return out, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
	}
	return "", &toolrejection.ToolReject{
		Code: "EDITOR_DOCUMENT_CHANGING",
		Data: map[string]any{"path": path, "attempts": editorDocumentRetries},
	}
}

func editorFileChange(tctx tools.ToolContext, resolved projectpaths.Resolved, preview tools.EditorDocumentPreview) tools.FileChange {
	return tools.FileChange{Path: resolved.Abs, Preview: api.ApprovalFileChange{
		Path: resolved.DisplayPath, RootID: resolved.Root.ID, Operation: "write",
		// The review carries the reference the call named, never the value it resolves.
		Before: tctx.Effects.Secrets.ReferenceEchoes(preview.Before.Text), After: tctx.Effects.Secrets.ReferenceEchoes(preview.After),
		BeforeSHA256: preview.Before.SHA256, AfterSHA256: preview.AfterSHA256,
		BeforeBytes: preview.BeforeBytes, AfterBytes: preview.AfterBytes,
	}}
}
