package toolhost

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/tools"
)

// editorDocumentsBridge adapts Painted Wolf's live tools.EditorDocuments
// service onto pythondocuments.EditorBridge so Portable Python guests read
// unsaved editor drafts and publish revision-checked edits through the editor.
type editorDocumentsBridge struct {
	docs      tools.EditorDocuments
	projectID string
	sessionID string
	turn      int
	callID    string
}

var _ pythondocuments.EditorBridge = (*editorDocumentsBridge)(nil)

func newEditorDocumentsBridge(tc tools.ToolContext) pythondocuments.EditorBridge {
	docs, ok := tools.ProjectDocuments(tc)
	if !ok || docs == nil {
		return nil
	}
	return &editorDocumentsBridge{
		docs:      docs,
		projectID: tc.Identity.ProjectID,
		sessionID: tc.Identity.SessionID,
		turn:      tc.Identity.UserTurn,
		callID:    tc.Identity.ToolCallID,
	}
}

func (b *editorDocumentsBridge) CaptureDraft(ctx context.Context, rootID, branch, relPath string) (pythondocuments.EditorDraft, bool, error) {
	if b == nil || b.docs == nil {
		return pythondocuments.EditorDraft{}, false, nil
	}
	doc, ok, err := b.docs.OpenDocument(ctx, b.projectID, sourcebranch.ID(branch), rootID, relPath)
	if err != nil {
		// Propagate capture errors so HostDocumentStore never falls back to stale disk bytes.
		return pythondocuments.EditorDraft{}, false, err
	}
	if !ok {
		return pythondocuments.EditorDraft{}, false, nil
	}
	return pythondocuments.EditorDraft{
		DocumentID: doc.ID,
		Revision:   strconv.FormatInt(doc.Revision, 10),
		Bytes:      []byte(doc.Text),
		BaseSHA256: doc.BaseSHA256,
		Dirty:      doc.Dirty,
		Absent:     doc.Absent,
	}, true, nil
}

func (b *editorDocumentsBridge) PublishDraft(
	ctx context.Context,
	rootID, branch, relPath string,
	baseRev pythondocuments.DocumentRevision,
	data []byte,
) (pythondocuments.DocumentRevision, bool, error) {
	if b == nil || b.docs == nil {
		return pythondocuments.DocumentRevision{}, false, nil
	}
	doc, ok, err := b.docs.OpenDocument(ctx, b.projectID, sourcebranch.ID(branch), rootID, relPath)
	if err != nil {
		return pythondocuments.DocumentRevision{}, false, err
	}
	if !ok {
		return pythondocuments.DocumentRevision{}, false, nil
	}

	expectedRev := doc.Revision
	if raw := strings.TrimSpace(baseRev.DocumentRevision); raw != "" {
		if parsed, parseErr := strconv.ParseInt(raw, 10, 64); parseErr == nil {
			expectedRev = parsed
		}
	}

	edit := tools.EditorDocumentEdit{
		DocumentID:       doc.ID,
		ExpectedRevision: expectedRev,
		Content:          string(data),
		OperationID:      uuid.NewString(),
		SessionID:        b.sessionID,
		Turn:             b.turn,
		ToolCallID:       b.callID,
		ToolName:         "python",
		Pin: &tools.EditorDocumentPin{
			Revision:   expectedRev,
			BaseSHA256: baseRev.DiskSHA256,
			Absent:     baseRev.Absent,
		},
	}
	res, err := b.docs.ApplyAgentEdit(ctx, b.projectID, edit)
	if err != nil {
		var fileChanged *tools.EditorFileChanged
		if errors.Is(err, tools.ErrEditorDocumentMoved) || errors.Is(err, tools.ErrEditorExecutionConflict) || errors.As(err, &fileChanged) {
			return pythondocuments.DocumentRevision{}, true, &pythondocuments.RevisionConflictError{
				Conflict: pythonruntime.RevisionConflict{
					Target: pythonruntime.CanonicalTarget{
						Namespace:  pythonruntime.NamespaceWorkspace,
						RootID:     rootID,
						Branch:     branch,
						DocumentID: doc.ID,
						RelPath:    relPath,
					},
					ExpectedRevision: baseRev.Identity(),
					ActualRevision:   strconv.FormatInt(doc.Revision, 10),
				},
			}
		}
		return pythondocuments.DocumentRevision{}, true, err
	}
	if res.PublicationError != nil {
		return pythondocuments.DocumentRevision{}, true, res.PublicationError
	}

	return pythondocuments.DocumentRevision{
		RootID:           rootID,
		Branch:           branch,
		DocumentID:       res.Document.ID,
		DocumentRevision: strconv.FormatInt(res.Document.Revision, 10),
		DiskSHA256:       pythondocuments.SHA256Hex(data),
		UnsavedDraft:     !res.Saved,
	}, true, nil
}
