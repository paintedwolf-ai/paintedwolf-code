package editoradmin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// failingSessions cannot read any chat.
type failingSessions struct{ session.Store }

func (failingSessions) Get(context.Context, string) (*wire.Session, error) {
	return nil, errors.New("session store offline")
}

func TestChangeHistoryPagesNewestFirstAndRevertsARestore(t *testing.T) {
	f := newEditorFixture(t)
	chat, err := f.sessions.Create(t.Context(), wire.CreateSessionRequest{Posture: wire.SessionPostureBuild}, f.project.ID)
	testutil.FailErr(t, "create chat", err)
	testutil.FailErr(t, "mark chat busy", f.sessions.SetSessionStatus(t.Context(), chat.ID, wire.SessionStatusBusy))
	testutil.FailErr(t, "start chat turn", f.sessions.AppendMessages(t.Context(), chat.ID, wire.Message{
		ID: "user-1", Role: wire.MessageRoleUser, Content: "edit the file",
		Origin: wire.MessageOriginUser, Authority: wire.ContentAuthorityUser,
		TrustTier: wire.ContentTrustTierTrusted, Visibility: wire.MessageVisibilityTranscript,
		CreatedAt: time.Now().UTC(),
	}))

	d := f.open(t, "a.txt", "window")
	d = f.discard(t, f.replace(t, d, "first\n"))
	d = f.replace(t, d, "second\n")
	affiliated := wire.EditorDocumentCommandRequest{SessionID: chat.ID, OperationID: uuid.NewString(), ClientID: "window", ExpectedRevision: d.Revision}
	d = decodeStatus[wire.EditorDocument](t, "affiliated discard", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/discard"), affiliated), http.StatusOK)

	first := decodeStatus[wire.EditorDocumentChanges](t, "first page", f.serve(t, http.MethodGet, f.documentPath(d.ID, "/changes?limit=1"), nil), http.StatusOK)
	if len(first.Changes) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want one change and a cursor", first)
	}
	newest := first.Changes[0]
	if newest.OperationID != affiliated.OperationID || newest.SessionID != chat.ID || newest.Turn != 1 || newest.ActorKind != "restore" {
		t.Fatalf("newest change = %+v, want the affiliated restore", newest)
	}
	next := f.documentPath(d.ID, "/changes?limit=1&cursor="+url.QueryEscape(first.NextCursor))
	second := decodeStatus[wire.EditorDocumentChanges](t, "second page", f.serve(t, http.MethodGet, next, nil), http.StatusOK)
	if len(second.Changes) != 1 || second.NextCursor != "" || second.Changes[0].Revision >= newest.Revision {
		t.Fatalf("second page = %+v, want the older restore and no cursor", second)
	}

	revert := wire.RevertEditorDocumentChangeRequest{ClientID: "window", OperationID: uuid.NewString(), Epoch: d.Epoch}
	reverted := decodeStatus[wire.EditorDocument](t, "revert", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/changes/"+newest.OperationID+"/revert"), revert), http.StatusOK)
	if reverted.Revision <= d.Revision || !reverted.Dirty {
		t.Fatalf("reverted = %+v, want the discarded draft restored", reverted)
	}
	revert.OperationID = uuid.NewString()
	expectCode(t, "revert twice", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/changes/"+newest.OperationID+"/revert"), revert), wire.ApiErrorCodeIdempotencyConflict)
	expectCode(t, "revert unknown", f.serve(t, http.MethodPost, f.documentPath(d.ID, "/changes/"+uuid.NewString()+"/revert"), revert), wire.ApiErrorCodeEditorDocumentNotFound)
}

func TestChangeHistoryRefusesForeignCursorsAndLimits(t *testing.T) {
	f := newEditorFixture(t)
	d := f.open(t, "a.txt", "window")
	expectCode(t, "limit", f.serve(t, http.MethodGet, f.documentPath(d.ID, "/changes?limit=0"), nil), wire.ApiErrorCodeInvalidQuery)
	expectCode(t, "garbled cursor", f.serve(t, http.MethodGet, f.documentPath(d.ID, "/changes?cursor=garbled"), nil), wire.ApiErrorCodeInvalidPageCursor)

	other := f.open(t, "staging.ts", "window")
	foreign, err := editorChangePages.Encode(pagecursor.Scope(f.project.ID, other.ID), editorChangeCursor{BeforeRevision: 5})
	testutil.FailErr(t, "encode another document's cursor", err)
	expectCode(t, "foreign cursor", f.serve(t, http.MethodGet, f.documentPath(d.ID, "/changes?cursor="+url.QueryEscape(foreign)), nil), wire.ApiErrorCodeInvalidPageCursor)
	exhausted, err := editorChangePages.Encode(pagecursor.Scope(f.project.ID, d.ID), editorChangeCursor{})
	testutil.FailErr(t, "encode exhausted cursor", err)
	expectCode(t, "exhausted cursor", f.serve(t, http.MethodGet, f.documentPath(d.ID, "/changes?cursor="+url.QueryEscape(exhausted)), nil), wire.ApiErrorCodeInvalidPageCursor)

	empty := decodeStatus[wire.EditorDocumentChanges](t, "no changes", f.serve(t, http.MethodGet, f.documentPath(d.ID, "/changes"), nil), http.StatusOK)
	if len(empty.Changes) != 0 || empty.NextCursor != "" {
		t.Fatalf("untouched history = %+v", empty)
	}
	expectCode(t, "missing document", f.serve(t, http.MethodGet, f.documentPath(uuid.NewString(), "/changes"), nil), wire.ApiErrorCodeEditorDocumentNotFound)
}

func TestUnreadableChatStopsAffiliatedCommands(t *testing.T) {
	f := newEditorFixture(t, withSessions(failingSessions{Store: store.NewMemory()}))
	d := f.open(t, "a.txt", "window")
	chat := uuid.NewString()
	command := wire.EditorDocumentCommandRequest{SessionID: chat, OperationID: uuid.NewString(), ClientID: "window", ExpectedRevision: d.Revision}
	for _, request := range []struct {
		method, suffix string
		body           any
	}{
		{http.MethodPut, "", wire.ReplaceEditorDocumentRequest{SessionID: &chat, OperationID: uuid.NewString(), ClientID: "window", ExpectedRevision: d.Revision, Content: "x", EOL: "lf"}},
		{http.MethodPost, "/updates", wire.SubmitEditorDocumentUpdateRequest{SessionID: chat, ClientID: "window"}},
		{http.MethodPost, "/discard", command},
		{http.MethodPost, "/reload", command},
		{http.MethodPost, "/save", wire.SaveEditorDocumentRequest{SessionID: chat, ClientID: "window", OperationID: uuid.NewString()}},
		{http.MethodPost, "/resolve", wire.ResolveEditorDocumentRequest{SessionID: chat, ClientID: "window", OperationID: uuid.NewString(), EOL: "lf"}},
		{http.MethodPost, "/changes/" + uuid.NewString() + "/revert", wire.RevertEditorDocumentChangeRequest{SessionID: chat, ClientID: "window", OperationID: uuid.NewString()}},
	} {
		expectCode(t, request.method+" "+request.suffix, f.serve(t, request.method, f.documentPath(d.ID, request.suffix), request.body), wire.ApiErrorCodeInternalError)
	}
	current, err := f.h.EditorDocuments.CurrentSnapshot(t.Context(), f.project.ID, d.ID)
	testutil.FailErr(t, "read document after refusals", err)
	if current.Revision != d.Revision {
		t.Fatalf("revision = %d, want %d — a refused command changed the draft", current.Revision, d.Revision)
	}
}

func TestCommandHistoryProjection(t *testing.T) {
	if editorCommandHistoryDTO(nil) != nil {
		t.Fatal("absent command history projected a value")
	}
	got := editorCommandHistoryDTO(&editordoc.CommandHistory{OperationID: "op", Epoch: 3, BeforeUpdate: []byte{1}, Update: []byte{2}})
	if got == nil || got.OperationID != "op" || got.Epoch != 3 || got.BeforeUpdate[0] != 1 || got.Update[0] != 2 {
		t.Fatalf("command history = %+v", got)
	}
	if heldAgentVersionID(&editordoc.Document{HeldAgentVersionID: " "}) != nil || *heldAgentVersionID(&editordoc.Document{HeldAgentVersionID: "v1"}) != "v1" {
		t.Fatal("held agent version projection")
	}
	if editorBaseContent(nil) != nil {
		t.Fatal("absent document projected a base")
	}
}
