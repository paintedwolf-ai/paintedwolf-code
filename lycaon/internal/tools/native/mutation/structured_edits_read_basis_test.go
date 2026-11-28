package mutation

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
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type fixedProjectRoots struct{ p *project.Project }

func (f fixedProjectRoots) Get(_ context.Context, projectID string) (*project.Project, error) {
	if f.p != nil && f.p.ID == projectID {
		return f.p, nil
	}
	return nil, editordoc.ErrNotFound
}

// testEditorAdapter mirrors app.editorDocumentsAdapter over a real editordoc.Service.
type testEditorAdapter struct {
	service *editordoc.Service
}

func (a testEditorAdapter) OpenDocument(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (tools.EditorDocumentText, bool, error) {
	d, ok, err := a.service.OpenText(ctx, projectID, branch, rootID, path)
	if err != nil || !ok {
		return tools.EditorDocumentText{}, false, err
	}
	sha, err := d.DraftSHA256()
	if err != nil {
		return tools.EditorDocumentText{}, false, err
	}
	return tools.EditorDocumentText{
		ID: d.ID, FileID: d.FileID, RootID: d.RootID, Path: d.Path,
		Revision: d.Revision, Text: d.Draft, SHA256: sha, BaseSHA256: d.BaseSHA256,
		Encoding: d.Encoding, EOL: d.EOL, Dirty: d.Dirty, Diverged: d.Diverged, Absent: d.Absent, ReadWhole: true,
	}, true, nil
}

func (a testEditorAdapter) FreezeAgentReads(projectID, sessionID string) map[string]documentcore.ReadBasis {
	return a.service.FreezeAgentReads(projectID, sessionID)
}

func (a testEditorAdapter) RememberAgentRead(projectID, sessionID string, doc tools.EditorDocumentText) {
	a.service.RememberAgentRead(projectID, sessionID, doc.ID, doc.Revision)
}

func (a testEditorAdapter) RememberAgentReadSpans(projectID, sessionID string, doc tools.EditorDocumentText, spans []documentcore.LineSpan) {
	a.service.RememberAgentReadSpans(projectID, sessionID, doc.ID, doc.Revision, spans)
}

func (a testEditorAdapter) AdvanceAgentRead(projectID, sessionID string, frozen *tools.AgentReadBases, edit tools.EditorDocumentEdit, doc tools.EditorDocumentText) {
	if doc.Text == strings.ReplaceAll(edit.Content, "\r\n", "\n") {
		if !doc.ReadWhole {
			a.service.DropAgentRead(projectID, sessionID, doc.ID)
			a.RememberAgentReadSpans(projectID, sessionID, doc, doc.ReadSpans)
		} else {
			a.service.RememberAgentRead(projectID, sessionID, doc.ID, doc.Revision)
		}
		frozen.AdvanceBasis(doc.ID, documentcore.ReadBasis{Revision: doc.Revision, Whole: doc.ReadWhole, Spans: doc.ReadSpans})
		return
	}
	a.service.DropAgentRead(projectID, sessionID, doc.ID)
	frozen.Forget(doc.ID)
}

func (a testEditorAdapter) AgentReadBase(ctx context.Context, projectID, sessionID, documentID string, frozen *tools.AgentReadBases) (tools.EditorDocumentText, error) {
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
	sha, err := d.DraftSHA256()
	if err != nil {
		return tools.EditorDocumentText{}, err
	}
	return tools.EditorDocumentText{
		ID: d.ID, FileID: d.FileID, RootID: d.RootID, Path: d.Path,
		Revision: d.Revision, Text: d.Draft, SHA256: sha, BaseSHA256: d.BaseSHA256,
		Encoding: d.Encoding, EOL: d.EOL, Dirty: d.Dirty, Diverged: d.Diverged, Absent: d.Absent,
		ReadWhole: d.ReadWhole, ReadSpans: append([]documentcore.LineSpan(nil), d.ReadSpans...),
	}, nil
}

func (a testEditorAdapter) PreviewAgentEdits(ctx context.Context, projectID string, edits []tools.EditorDocumentEdit) ([]tools.EditorDocumentPreview, error) {
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
		out[i] = tools.EditorDocumentPreview{
			Before:      tools.EditorDocumentText{ID: p.Before.ID, Revision: p.Before.Revision, Text: p.Before.Draft, SHA256: sha},
			After:       p.After,
			AfterSHA256: p.AfterSHA256,
			BeforeBytes: p.BeforeBytes,
			AfterBytes:  p.AfterBytes,
		}
	}
	return out, nil
}

func (a testEditorAdapter) ApplyAgentEdit(ctx context.Context, projectID string, edit tools.EditorDocumentEdit) (tools.EditorDocumentApplied, error) {
	res, err := a.service.ApplyAgentEdit(ctx, editordoc.AgentEdit{
		ProjectID: projectID, DocumentID: edit.DocumentID, ExpectedRevision: edit.ExpectedRevision, ReviewedRevision: edit.ReviewedRevision,
		Content: edit.Content, OperationID: edit.OperationID, SessionID: edit.SessionID, Turn: edit.Turn,
		ToolCallID: edit.ToolCallID, ToolName: edit.ToolName,
	})
	if err != nil {
		if errors.Is(err, editordoc.ErrRevisionConflict) {
			return tools.EditorDocumentApplied{}, tools.ErrEditorDocumentMoved
		}
		return tools.EditorDocumentApplied{}, err
	}
	sha, _ := res.Document.DraftSHA256()
	return tools.EditorDocumentApplied{
		Saved: res.Saved, HeldVersionID: res.HeldVersionID, PublicationError: res.PublicationError,
		Document: tools.EditorDocumentText{
			ID: res.Document.ID, FileID: res.Document.FileID, RootID: res.Document.RootID, Path: res.Document.Path,
			Revision: res.Document.Revision, Text: res.Document.Draft, SHA256: sha, BaseSHA256: res.Document.BaseSHA256,
			Encoding: res.Document.Encoding, EOL: res.Document.EOL, Dirty: res.Document.Dirty, Diverged: res.Document.Diverged, Absent: res.Document.Absent,
		},
	}, nil
}

func (a testEditorAdapter) ApplyAgentEdits(context.Context, string, []tools.EditorDocumentEdit) ([]tools.EditorDocumentApplied, error) {
	return nil, nil
}
func (a testEditorAdapter) VisitUnsavedDocuments(context.Context, string, sourcebranch.ID, string, string, bool, func(tools.EditorDocumentState) error) error {
	return nil
}
func (a testEditorAdapter) RetainRevision(context.Context, string, string, int64) error { return nil }
func (a testEditorAdapter) SaveForAgentOperation(context.Context, string, sourcebranch.ID, string, string, bool, tools.EditorAgentCall) error {
	return nil
}
func (a testEditorAdapter) SaveForAgentLaunch(context.Context, string, sourcebranch.ID, tools.EditorAgentCall, func(string, int64) bool) (tools.EditorLaunchSave, error) {
	return tools.EditorLaunchSave{}, nil
}
func (a testEditorAdapter) DirtyDocuments(context.Context, string, sourcebranch.ID) ([]tools.EditorDocumentPath, error) {
	return nil, nil
}
func (a testEditorAdapter) VisitAgentPublications(context.Context, string, sourcebranch.ID, string, int, string, func(tools.EditorDocumentPublicationCandidate) error) error {
	return nil
}

func wireRealEditorDocs(t *testing.T, h *harness) (*editordoc.Service, *sourceledger.Store) {
	t.Helper()
	ledger := sourceledger.New(h.db, t.TempDir(), sourceledger.Timing{})
	store := editordoc.NewStore(h.db)
	svc := editordoc.New(store, ledger, ledger.History, fixedProjectRoots{p: h.project})
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	h.tc.Source.SourceWorkspaceKind = api.SourceWorkspaceKindProject
	h.tc.Source.EditorDocuments = testEditorAdapter{service: svc}
	h.tc.Identity.SessionID = "chat-structured"
	h.tc.Identity.UserTurn = 1
	return svc, ledger
}

func requireRejectCode(t *testing.T, r run, want string) {
	t.Helper()
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != want {
		t.Fatalf("got err=%v result=%+v, want rejection code %s", r.err, r.result, want)
	}
}

func TestStructuredEditsEnforceConversationalReadBasisAndRebaseWithoutAutosaveOnOverlap(t *testing.T) {
	initial := "L1_alpha\nL2_beta\nL3_gamma\nL4_delta\nL5_epsilon\n"
	h := newHarness(t, map[string]string{
		"doc.txt": initial,
		"main.go": "package main\n\nfunc calc() int {\n\treturn 1 + 2\n}\n",
	})
	svc, _ := wireRealEditorDocs(t, h)

	// 1. Without any prior read, edit / replace-lines / code-rewrite / tee MUST refuse
	// with EDITOR_DOCUMENT_READ_REQUIRED and leave disk untouched.
	requireRejectCode(t, h.run("", "edit", "--path", "doc.txt", "--old-string", "L2_beta", "--new-string", "L2_NEW"), "EDITOR_DOCUMENT_READ_REQUIRED")
	requireRejectCode(t, h.run("", "replace-lines", "--path", "doc.txt", "--start", "2", "--end", "2", "--content", "L2_NEW"), "EDITOR_DOCUMENT_READ_REQUIRED")
	requireRejectCode(t, h.run("", "code-rewrite", "--path", "main.go", "--pattern", "return 1 + 2", "--rewrite", "return 3"), "EDITOR_DOCUMENT_READ_REQUIRED")
	requireRejectCode(t, h.run("rewritten\n", "tee", "doc.txt"), "EDITOR_DOCUMENT_READ_REQUIRED")
	if h.read("doc.txt") != initial {
		t.Fatalf("refused edits mutated doc.txt: %q", h.read("doc.txt"))
	}

	// 2. Partial span read [1..1] does NOT authorize editing line 5 or whole-file tee/code-rewrite.
	doc, ok, err := svc.OpenText(t.Context(), h.project.ID, sourcebranch.Trunk, h.project.Roots[0].ID, "doc.txt")
	testutil.FailErr(t, "open doc.txt", err)
	if !ok {
		t.Fatal("expected doc.txt")
	}
	svc.RememberAgentReadSpans(h.project.ID, h.tc.Identity.SessionID, doc.ID, doc.Revision, []documentcore.LineSpan{{StartLine: 1, EndLine: 1}})
	requireRejectCode(t, h.run("", "edit", "--path", "doc.txt", "--old-string", "L5_epsilon", "--new-string", "L5_NEW"), "EDITOR_DOCUMENT_READ_REQUIRED")
	requireRejectCode(t, h.run("", "replace-lines", "--path", "doc.txt", "--start", "5", "--end", "5", "--content", "L5_NEW"), "EDITOR_DOCUMENT_READ_REQUIRED")
	requireRejectCode(t, h.run("rewritten\n", "tee", "doc.txt"), "EDITOR_DOCUMENT_READ_REQUIRED")

	// 3. Full read + overlapping person unsaved draft on line 3:
	// edit MUST refuse with EDITOR_DOCUMENT_CHANGING and MUST NOT autosave person's draft to disk!
	svc.RememberAgentRead(h.project.ID, h.tc.Identity.SessionID, doc.ID, doc.Revision)
	_, err = svc.ReplaceSnapshot(t.Context(), doc.ID, h.project.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{ClientID: "window-1", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision},
		Content:         "L1_alpha\nL2_beta\nL3_PERSON_CONFLICT\nL4_delta\nL5_epsilon\n",
		EOL:             "lf",
	})
	testutil.FailErr(t, "person overlapping draft", err)
	requireRejectCode(t, h.run("", "edit", "--path", "doc.txt", "--old-string", "L3_gamma", "--new-string", "L3_AGENT_CONFLICT"), "EDITOR_DOCUMENT_CHANGING")
	if got := h.read("doc.txt"); got != initial {
		t.Fatalf("overlapping edit mutated disk (premature autosave): got=%q want=%q", got, initial)
	}
	// Retry without rereading keeps the original read basis (still sees L3_gamma, NOT L3_PERSON_CONFLICT).
	requireRejectCode(t, h.run("", "edit", "--path", "doc.txt", "--old-string", "L3_PERSON_CONFLICT", "--new-string", "L3_RETRY"), "EDIT_OLD_STRING_NOT_FOUND")

	// 4. Non-overlapping edit on line 5 rebases cleanly against person's line 3 draft,
	// preserves both edits on disk, and withdraws the read basis because the merged result
	// contains the person's text!
	r := h.run("", "edit", "--path", "doc.txt", "--old-string", "L5_epsilon", "--new-string", "L5_AGENT")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("non-overlapping rebase failed: err=%v result=%+v", r.err, r.result)
	}
	wantCombined := "L1_alpha\nL2_beta\nL3_PERSON_CONFLICT\nL4_delta\nL5_AGENT\n"
	if got := h.read("doc.txt"); got != wantCombined {
		t.Fatalf("rebased disk = %q, want %q", got, wantCombined)
	}
	// Verify distinct contributor records exist (person + agent) in source_text_contributions.
	var origins []string
	rows, qerr := h.db.QueryContext(t.Context(), `SELECT DISTINCT origin FROM source_text_contributions WHERE document_id = ? ORDER BY origin`, doc.ID)
	testutil.FailErr(t, "query contributions", qerr)
	defer rows.Close()
	for rows.Next() {
		var o string
		testutil.FailErr(t, "scan origin", rows.Scan(&o))
		origins = append(origins, o)
	}
	if len(origins) < 2 {
		t.Fatalf("expected distinct person+agent contributor origins in source_text_contributions, got %v", origins)
	}
	// Merged result withdrew the read basis -> follow-up edit without reread MUST refuse!
	requireRejectCode(t, h.run("", "edit", "--path", "doc.txt", "--old-string", "L4_delta", "--new-string", "L4_AGENT"), "EDITOR_DOCUMENT_READ_REQUIRED")
}
