package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func getSourceWalk(t *testing.T, srv *Server, projectID string, query url.Values) (*httptest.ResponseRecorder, wire.SourceWalkResponse) {
	t.Helper()
	req := newAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/source/walk?"+query.Encode(), nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var page wire.SourceWalkResponse
	if w.Code == http.StatusOK {
		testutil.FailErr(t, "decode walk", json.Unmarshal(w.Body.Bytes(), &page))
	}
	return w, page
}

// `turn:{session}` reads the chat's current turn and names the ordinal it read.
func TestSourceWalkResolvesTheChatsCurrentTurn(t *testing.T) {
	ledger, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	sess, err := srv.sessionStore.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID, Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create session", err)
	idle, err := srv.sessionStore.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID, Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create idle session", err)
	testutil.FailErr(t, "append prompts", srv.sessionStore.AppendMessages(t.Context(), sess.ID,
		wire.Message{ID: "prompt-1", Role: wire.MessageRoleUser, Content: "first"},
		wire.Message{ID: "prompt-2", Role: wire.MessageRoleUser, Content: "second"},
	))
	rootID := p.Roots[0].ID
	for turn, path := range map[int]string{1: "first.go", 2: "second.go"} {
		testutil.FailErr(t, "record "+path, ledger.Record(t.Context(), sourceledger.RecordInput{
			ProjectID: p.ID, RootID: rootID, Path: path,
			Op: wire.SourceChangeOpCreate, Origin: wire.SourceChangeOriginAgent,
			SessionID: sess.ID, Turn: turn, OperationID: "turn-write-" + path, After: []byte(path),
		}))
	}

	w, page := getSourceWalk(t, srv, p.ID, url.Values{"baseline": {"turn:" + sess.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if page.Baseline != "turn:"+sess.ID+",2" {
		t.Fatalf("baseline = %q, want the current turn spelled out", page.Baseline)
	}
	if len(page.Files) != 1 || page.Files[0].Path != "second.go" {
		t.Fatalf("files = %+v, want only the current turn's write", page.Files)
	}

	// The resolved baseline reads the same range again.
	w, again := getSourceWalk(t, srv, p.ID, url.Values{"baseline": {page.Baseline}, "session_id": {sess.ID}})
	if w.Code != http.StatusOK || again.Baseline != page.Baseline || len(again.Files) != 1 {
		t.Fatalf("resolved baseline status = %d page = %+v", w.Code, again)
	}

	w, empty := getSourceWalk(t, srv, p.ID, url.Values{"baseline": {"turn:" + idle.ID}})
	if w.Code != http.StatusOK || empty.Baseline != "turn:"+idle.ID+",0" || len(empty.Files) != 0 {
		t.Fatalf("chat with no turn: status = %d page = %+v", w.Code, empty)
	}
}

// A chat baseline reads that chat's workspace, so another chat cannot supply it.
func TestSourceWalkRejectsAChatBaselineReadThroughAnotherChat(t *testing.T) {
	_, ledgerDB, withLedger := testSourceLedger(t)
	srv := newTestServer(t, withLedger)
	p := createProjectForTest(t, srv, t.TempDir())
	mirrorLedgerProject(t, ledgerDB, p)
	owner, err := srv.sessionStore.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID, Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create owner", err)
	other, err := srv.sessionStore.Create(t.Context(), wire.CreateSessionRequest{ProjectID: p.ID, Posture: wire.SessionPostureBuild}, p.ID)
	testutil.FailErr(t, "create other", err)

	for name, baseline := range map[string]string{
		"current turn": "turn:" + owner.ID,
		"turn":         "turn:" + owner.ID + ",1",
		"session":      "session:" + owner.ID,
	} {
		t.Run(name, func(t *testing.T) {
			w, _ := getSourceWalk(t, srv, p.ID, url.Values{"baseline": {baseline}, "session_id": {other.ID}})
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
			}
			w, _ = getSourceWalk(t, srv, p.ID, url.Values{"baseline": {baseline}, "session_id": {owner.ID}})
			if w.Code != http.StatusOK {
				t.Fatalf("same chat status = %d body = %s", w.Code, w.Body.String())
			}
		})
	}
	w, _ := getSourceWalk(t, srv, p.ID, url.Values{"baseline": {"turn:00000000-0000-4000-8000-000000000000"}})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown chat status = %d body = %s", w.Code, w.Body.String())
	}
}
