package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type jqFixedRoots struct{ p *project.Project }

func (f jqFixedRoots) Get(_ context.Context, projectID string) (*project.Project, error) {
	if f.p != nil && f.p.ID == projectID {
		return f.p, nil
	}
	return nil, editordoc.ErrNotFound
}

type jqEditorAdapter struct{ service *editordoc.Service }

func (a jqEditorAdapter) OpenDocument(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (tools.EditorDocumentText, bool, error) {
	d, ok, err := a.service.OpenText(ctx, projectID, branch, rootID, path)
	if err != nil || !ok {
		return tools.EditorDocumentText{}, false, err
	}
	sha, _ := d.DraftSHA256()
	return tools.EditorDocumentText{ID: d.ID, FileID: d.FileID, RootID: d.RootID, Path: d.Path, Revision: d.Revision, Text: d.Draft, SHA256: sha, BaseSHA256: d.BaseSHA256, Encoding: d.Encoding, EOL: d.EOL, Dirty: d.Dirty, ReadWhole: true}, true, nil
}
func (a jqEditorAdapter) FreezeAgentReads(projectID, sessionID string) map[string]documentcore.ReadBasis {
	return a.service.FreezeAgentReads(projectID, sessionID)
}
func (a jqEditorAdapter) RememberAgentRead(projectID, sessionID string, doc tools.EditorDocumentText) {
	a.service.RememberAgentRead(projectID, sessionID, doc.ID, doc.Revision)
}
func (a jqEditorAdapter) RememberAgentReadSpans(projectID, sessionID string, doc tools.EditorDocumentText, spans []documentcore.LineSpan) {
	a.service.RememberAgentReadSpans(projectID, sessionID, doc.ID, doc.Revision, spans)
}
func (a jqEditorAdapter) AdvanceAgentRead(projectID, sessionID string, frozen *tools.AgentReadBases, edit tools.EditorDocumentEdit, doc tools.EditorDocumentText) {
	if doc.Text == strings.ReplaceAll(edit.Content, "\r\n", "\n") {
		a.service.RememberAgentRead(projectID, sessionID, doc.ID, doc.Revision)
		frozen.AdvanceBasis(doc.ID, documentcore.ReadBasis{Revision: doc.Revision, Whole: true})
		return
	}
	a.service.DropAgentRead(projectID, sessionID, doc.ID)
	frozen.Forget(doc.ID)
}
func (a jqEditorAdapter) AgentReadBase(ctx context.Context, projectID, sessionID, documentID string, frozen *tools.AgentReadBases) (tools.EditorDocumentText, error) {
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
	sha, _ := d.DraftSHA256()
	return tools.EditorDocumentText{ID: d.ID, FileID: d.FileID, RootID: d.RootID, Path: d.Path, Revision: d.Revision, Text: d.Draft, SHA256: sha, BaseSHA256: d.BaseSHA256, Encoding: d.Encoding, EOL: d.EOL, Dirty: d.Dirty, ReadWhole: d.ReadWhole, ReadSpans: d.ReadSpans}, nil
}
func (a jqEditorAdapter) PreviewAgentEdits(ctx context.Context, projectID string, edits []tools.EditorDocumentEdit) ([]tools.EditorDocumentPreview, error) {
	in := make([]editordoc.AgentEdit, len(edits))
	for i, e := range edits {
		in[i] = editordoc.AgentEdit{ProjectID: projectID, DocumentID: e.DocumentID, ExpectedRevision: e.ExpectedRevision, Content: e.Content, OperationID: e.OperationID}
	}
	previews, err := a.service.PreviewAgentEdits(ctx, in)
	if err != nil {
		if errors.Is(err, editordoc.ErrRevisionConflict) {
			return nil, tools.ErrEditorDocumentMoved
		}
		return nil, err
	}
	out := make([]tools.EditorDocumentPreview, len(previews))
	for i, p := range previews {
		sha, _ := p.Before.DraftSHA256()
		out[i] = tools.EditorDocumentPreview{Before: tools.EditorDocumentText{ID: p.Before.ID, Revision: p.Before.Revision, Text: p.Before.Draft, SHA256: sha}, After: p.After, AfterSHA256: p.AfterSHA256, BeforeBytes: p.BeforeBytes, AfterBytes: p.AfterBytes}
	}
	return out, nil
}
func (a jqEditorAdapter) ApplyAgentEdit(ctx context.Context, projectID string, edit tools.EditorDocumentEdit) (tools.EditorDocumentApplied, error) {
	res, err := a.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{ProjectID: projectID, DocumentID: edit.DocumentID, ExpectedRevision: edit.ExpectedRevision, ReviewedRevision: edit.ReviewedRevision, Content: edit.Content, OperationID: edit.OperationID, SessionID: edit.SessionID, Turn: edit.Turn, ToolCallID: edit.ToolCallID, ToolName: edit.ToolName})
	if err != nil {
		if errors.Is(err, editordoc.ErrRevisionConflict) {
			return tools.EditorDocumentApplied{}, tools.ErrEditorDocumentMoved
		}
		return tools.EditorDocumentApplied{}, err
	}
	sha, _ := res.Document.DraftSHA256()
	return tools.EditorDocumentApplied{Saved: res.Saved, Document: tools.EditorDocumentText{ID: res.Document.ID, FileID: res.Document.FileID, RootID: res.Document.RootID, Path: res.Document.Path, Revision: res.Document.Revision, Text: res.Document.Draft, SHA256: sha, BaseSHA256: res.Document.BaseSHA256, Encoding: res.Document.Encoding, EOL: res.Document.EOL, Dirty: res.Document.Dirty}}, nil
}
func (a jqEditorAdapter) ApplyAgentEdits(context.Context, string, []tools.EditorDocumentEdit) ([]tools.EditorDocumentApplied, error) {
	return nil, nil
}
func (a jqEditorAdapter) VisitUnsavedDocuments(context.Context, string, sourcebranch.ID, string, string, bool, func(tools.EditorDocumentState) error) error {
	return nil
}
func (a jqEditorAdapter) RetainRevision(context.Context, string, string, int64) error { return nil }
func (a jqEditorAdapter) SaveForAgentOperation(context.Context, string, sourcebranch.ID, string, string, bool, tools.EditorAgentCall) error {
	return nil
}
func (a jqEditorAdapter) SaveForAgentLaunch(context.Context, string, sourcebranch.ID, tools.EditorAgentCall, func(string, int64) bool) (tools.EditorLaunchSave, error) {
	return tools.EditorLaunchSave{}, nil
}
func (a jqEditorAdapter) DirtyDocuments(context.Context, string, sourcebranch.ID) ([]tools.EditorDocumentPath, error) {
	return nil, nil
}
func (a jqEditorAdapter) VisitAgentPublications(context.Context, string, sourcebranch.ID, string, int, string, func(tools.EditorDocumentPublicationCandidate) error) error {
	return nil
}

func TestJqEditEnforcesConversationalReadBasisAndRebase(t *testing.T) {
	initial := "{\n  \"keep\": \"v1\",\n  \"value\": \"beta\"\n}\n"
	h := newHarness(t, map[string]string{"settings.json": initial})
	ledger := h.tc.Source.SourceLedger.(*sourceledger.Store)
	proj := project.Project{ID: "p1", Roots: []project.Root{{ID: "r1", Path: h.root}}}
	store := editordoc.NewStore(ledger.LedgerDB())
	svc := editordoc.New(store, ledger, ledger.History, jqFixedRoots{p: &proj})
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	h.tc.Source.SourceWorkspaceKind = api.SourceWorkspaceKindProject
	h.tc.Source.EditorDocuments = jqEditorAdapter{service: svc}
	h.tc.Identity.SessionID = "chat-jq"

	// 1. Without a full read, jq-edit MUST refuse with EDITOR_DOCUMENT_READ_REQUIRED and leave disk untouched.
	h.exec("", "jq-edit", "--path", "settings.json", "--query", `.value = "BETA"`).refused(t, "no read", "EDITOR_DOCUMENT_READ_REQUIRED")
	if h.disk("settings.json") != initial {
		t.Fatalf("refused jq-edit mutated disk: %q", h.disk("settings.json"))
	}

	// 2. Partial span read does NOT authorize jq-edit.
	doc, ok, err := svc.OpenText(t.Context(), proj.ID, sourcebranch.Trunk, proj.Roots[0].ID, "settings.json")
	testutil.FailErr(t, "open settings.json", err)
	if !ok {
		t.Fatal("expected settings.json")
	}
	svc.RememberAgentReadSpans(proj.ID, "chat-jq", doc.ID, doc.Revision, []documentcore.LineSpan{{StartLine: 1, EndLine: 2}})
	h.exec("", "jq-edit", "--path", "settings.json", "--query", `.value = "BETA"`).refused(t, "partial read", "EDITOR_DOCUMENT_READ_REQUIRED")

	// 3. Full read authorizes jq-edit and rebases non-overlapping person draft on "keep".
	svc.RememberAgentRead(proj.ID, "chat-jq", doc.ID, doc.Revision)
	_, err = svc.ReplaceSnapshot(t.Context(), doc.ID, proj.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
		Content:         "{\n  \"keep\": \"PERSON\",\n  \"value\": \"beta\"\n}\n",
		EOL:             "lf",
	})
	testutil.FailErr(t, "person draft", err)
	h.exec("", "jq-edit", "--path", "settings.json", "--query", `.value = "BETA"`).expect(t, "rebased jq-edit", "", 0)
	want := "{\n  \"keep\": \"PERSON\",\n  \"value\": \"BETA\"\n}\n"
	if got := h.disk("settings.json"); got != want {
		t.Fatalf("rebased settings.json = %q, want %q", got, want)
	}
}
