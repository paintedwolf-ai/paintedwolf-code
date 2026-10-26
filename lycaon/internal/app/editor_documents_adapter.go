package app

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/tools"
)

// editorDocumentsAdapter gives tools access to shared text, including unsaved edits.
type editorDocumentsAdapter struct {
	service *editordoc.Service
}

func (a editorDocumentsAdapter) OpenDocument(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (tools.EditorDocumentText, bool, error) {
	d, ok, err := a.service.OpenText(ctx, projectID, branch, rootID, path)
	if err != nil || !ok {
		return tools.EditorDocumentText{}, false, err
	}
	text, err := editorDocumentText(d)
	if err != nil {
		return tools.EditorDocumentText{}, false, err
	}
	return text, true, nil
}

func (a editorDocumentsAdapter) VisitUnsavedDocuments(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string, descendants bool, visit func(tools.EditorDocumentState) error) error {
	return a.service.VisitUnsavedDocuments(ctx, projectID, branch, rootID, path, descendants, func(d editordoc.DocumentPathState) error {
		return visit(tools.EditorDocumentState{ID: d.ID, RootID: d.RootID, Path: d.Path, Revision: d.Revision, BaseSHA256: d.BaseSHA256, Absent: d.Absent})
	})
}

func (a editorDocumentsAdapter) DirtyDocuments(ctx context.Context, projectID string, branch sourcebranch.ID) ([]tools.EditorDocumentPath, error) {
	documents, err := a.service.DirtyDocuments(ctx, projectID, branch)
	if err != nil {
		return nil, err
	}
	out := make([]tools.EditorDocumentPath, 0, len(documents))
	for _, d := range documents {
		out = append(out, tools.EditorDocumentPath{RootID: d.RootID, Path: d.Path, Text: d.Draft})
	}
	return out, nil
}

func agentEditInput(projectID string, edit tools.EditorDocumentEdit) editordoc.AgentEdit {
	return editordoc.AgentEdit{
		ProjectID: projectID, DocumentID: edit.DocumentID, ExpectedRevision: edit.ExpectedRevision, ReviewedRevision: edit.ReviewedRevision,
		Content: edit.Content, OperationID: edit.OperationID, SessionID: edit.SessionID, Turn: edit.Turn,
		ToolCallID: edit.ToolCallID, ToolName: edit.ToolName, Pin: executionPin(edit.Pin),
	}
}

func executionPin(pin *tools.EditorDocumentPin) *editordoc.ExecutionPin {
	if pin == nil {
		return nil
	}
	return &editordoc.ExecutionPin{Revision: pin.Revision, DiskSHA256: pin.BaseSHA256, Absent: pin.Absent}
}

// documentEditError keeps a pinned program conflict distinct from the moved
// sentinel conversational edits retry on.
func documentEditError(err error) error {
	switch {
	case errors.Is(err, editordoc.ErrExecutionConflict):
		return errors.Join(tools.ErrEditorExecutionConflict, err)
	case errors.Is(err, editordoc.ErrRevisionConflict):
		return tools.ErrEditorDocumentMoved
	}
	return err
}

func appliedEdit(result *editordoc.AgentEditResult) (tools.EditorDocumentApplied, error) {
	text, err := editorDocumentText(result.Document)
	if err != nil {
		return tools.EditorDocumentApplied{}, err
	}
	return tools.EditorDocumentApplied{Document: text, Saved: result.Saved,
		HeldVersionID: result.HeldVersionID, PublicationError: result.PublicationError}, nil
}

func (a editorDocumentsAdapter) ApplyAgentEdit(ctx context.Context, projectID string, edit tools.EditorDocumentEdit) (tools.EditorDocumentApplied, error) {
	result, err := a.service.ApplyAgentEdit(ctx, agentEditInput(projectID, edit))
	if err != nil {
		return tools.EditorDocumentApplied{}, documentEditError(err)
	}
	return appliedEdit(result)
}

func editorDocumentText(d *editordoc.Document) (tools.EditorDocumentText, error) {
	sha, err := d.DraftSHA256()
	if err != nil {
		return tools.EditorDocumentText{}, err
	}
	base := d.BaseSHA256
	if d.Absent {
		base = ""
	}
	return tools.EditorDocumentText{
		ID: d.ID, Revision: d.Revision, Text: d.Draft, SHA256: sha, BaseSHA256: base, Encoding: d.Encoding, EOL: d.EOL, Dirty: d.Dirty, Diverged: d.Diverged, Absent: d.Absent, ReadWhole: true,
	}, nil
}

func (a editorDocumentsAdapter) ApplyAgentEdits(ctx context.Context, projectID string, edits []tools.EditorDocumentEdit) ([]tools.EditorDocumentApplied, error) {
	inputs := make([]editordoc.AgentEdit, len(edits))
	for i, edit := range edits {
		inputs[i] = agentEditInput(projectID, edit)
	}
	results, err := a.service.ApplyAgentEdits(ctx, inputs)
	if err != nil {
		return nil, documentEditError(err)
	}
	applied := make([]tools.EditorDocumentApplied, len(results))
	for i, result := range results {
		if applied[i], err = appliedEdit(result); err != nil {
			return nil, err
		}
	}
	return applied, nil
}

func (a editorDocumentsAdapter) PreviewAgentEdits(ctx context.Context, projectID string, edits []tools.EditorDocumentEdit) ([]tools.EditorDocumentPreview, error) {
	inputs := make([]editordoc.AgentEdit, len(edits))
	for i, edit := range edits {
		inputs[i] = editordoc.AgentEdit{ProjectID: projectID, DocumentID: edit.DocumentID,
			ExpectedRevision: edit.ExpectedRevision, Content: edit.Content, OperationID: edit.OperationID, Pin: executionPin(edit.Pin)}
	}
	previews, err := a.service.PreviewAgentEdits(ctx, inputs)
	if err != nil {
		return nil, documentEditError(err)
	}
	out := make([]tools.EditorDocumentPreview, len(previews))
	for i, preview := range previews {
		before, err := editorDocumentText(preview.Before)
		if err != nil {
			return nil, err
		}
		out[i] = tools.EditorDocumentPreview{Before: before, After: preview.After, AfterSHA256: preview.AfterSHA256, BeforeBytes: preview.BeforeBytes, AfterBytes: preview.AfterBytes}
	}
	return out, nil
}

func (a editorDocumentsAdapter) RememberAgentRead(projectID, sessionID string, document tools.EditorDocumentText) {
	a.service.RememberAgentRead(projectID, sessionID, document.ID, document.Revision)
}

func (a editorDocumentsAdapter) RememberAgentReadSpans(projectID, sessionID string, document tools.EditorDocumentText, spans []documentcore.LineSpan) {
	converted := make([]documentcore.LineSpan, len(spans))
	for i, s := range spans {
		converted[i] = documentcore.LineSpan{StartLine: s.StartLine, EndLine: s.EndLine}
	}
	a.service.RememberAgentReadSpans(projectID, sessionID, document.ID, document.Revision, converted)
}

func (a editorDocumentsAdapter) VisitAgentPublications(ctx context.Context, projectID string, branch sourcebranch.ID, sessionID string, turn int, toolCallID string, visit func(tools.EditorDocumentPublicationCandidate) error) error {
	return a.service.VisitAgentPublications(ctx, projectID, branch, sessionID, turn, toolCallID, func(state editordoc.DocumentPathState, load func(context.Context) (*editordoc.Document, func(context.Context) (*editordoc.Document, error), error)) error {
		return visit(tools.EditorDocumentPublicationCandidate{DraftBytes: state.DraftBytes, BaseBytes: state.BaseBytes, Load: func(ctx context.Context) (tools.EditorDocumentPublication, error) {
			doc, publish, err := load(ctx)
			if err != nil {
				return tools.EditorDocumentPublication{}, documentEditError(err)
			}
			text, err := editorDocumentText(doc)
			if err != nil {
				return tools.EditorDocumentPublication{}, err
			}
			return tools.EditorDocumentPublication{RootID: doc.RootID, Path: doc.Path, Document: text, BaseBytes: state.BaseBytes, Publish: func(ctx context.Context) (tools.EditorDocumentText, error) {
				saved, err := publish(ctx)
				if err != nil {
					return tools.EditorDocumentText{}, documentEditError(err)
				}
				return editorDocumentText(saved)
			}}, nil
		}})
	})
}

func (a editorDocumentsAdapter) AgentReadBase(ctx context.Context, projectID, sessionID, documentID string, frozen *tools.AgentReadBases) (tools.EditorDocumentText, error) {
	var lookup editordoc.ReadBasisLookup
	if frozen != nil {
		lookup = frozen.Basis
	}
	d, err := a.service.AgentReadBase(ctx, projectID, sessionID, documentID, lookup)
	if errors.Is(err, editordoc.ErrAgentReadRequired) {
		return tools.EditorDocumentText{}, tools.ErrEditorReadRequired
	}
	if err != nil {
		return tools.EditorDocumentText{}, err
	}
	text, conversionErr := editorDocumentText(d.Document)
	text.ReadWhole = d.ReadWhole
	text.ReadSpans = append([]documentcore.LineSpan(nil), d.ReadSpans...)
	return text, conversionErr
}

// AdvanceAgentRead moves the basis to text the agent authored in full and
// withdraws it when the accepted result carries anyone else's text.
func (a editorDocumentsAdapter) AdvanceAgentRead(projectID, sessionID string, frozen *tools.AgentReadBases, edit tools.EditorDocumentEdit, document tools.EditorDocumentText) {
	if document.Text == strings.ReplaceAll(edit.Content, "\r\n", "\n") {
		if !document.ReadWhole {
			a.service.DropAgentRead(projectID, sessionID, document.ID)
			a.RememberAgentReadSpans(projectID, sessionID, document, document.ReadSpans)
		} else {
			a.service.RememberAgentRead(projectID, sessionID, document.ID, document.Revision)
		}
		frozen.AdvanceBasis(document.ID, documentcore.ReadBasis{Revision: document.Revision, Whole: document.ReadWhole, Spans: document.ReadSpans})
		return
	}
	a.service.DropAgentRead(projectID, sessionID, document.ID)
	frozen.Forget(document.ID)
}

func (a editorDocumentsAdapter) FreezeAgentReads(projectID, sessionID string) map[string]documentcore.ReadBasis {
	return a.service.FreezeAgentReads(projectID, sessionID)
}
