package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGetSessionMessages(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	created := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	msgsReq := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages", nil)
	msgsRec := httptest.NewRecorder()
	srv.ServeHTTP(msgsRec, msgsReq)

	if msgsRec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", msgsRec.Code, msgsRec.Body.String())
	}
	var page wire.SessionTranscriptPage
	if err := json.Unmarshal(msgsRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if page.Messages == nil {
		t.Fatal("expected non-nil empty array")
	}
}

func TestGetSessionMessagesPagination(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t)
	created := createSessionAtPathOnServer(t, srv, dir, wire.SessionPostureBuild)

	msgsReq := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?limit=5", nil)
	msgsRec := httptest.NewRecorder()
	srv.ServeHTTP(msgsRec, msgsReq)
	if msgsRec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", msgsRec.Code, msgsRec.Body.String())
	}
	var page wire.SessionTranscriptPage
	if err := json.Unmarshal(msgsRec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Messages == nil {
		t.Fatal("expected non-nil messages")
	}
	if page.BeforeCursor != "" || page.AfterCursor != "" {
		t.Fatalf("empty session cursors: before=%v after=%v", page.BeforeCursor, page.AfterCursor)
	}

	both := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?before=1&after=2", nil)
	bothRec := httptest.NewRecorder()
	srv.ServeHTTP(bothRec, both)
	if bothRec.Code != http.StatusBadRequest {
		t.Fatalf("both cursors status = %d, want 400", bothRec.Code)
	}
}

// A window opens beside a message the client holds, or at either end.
func TestGetSessionMessagesPositionsWindows(t *testing.T) {
	sessions := storetest.New(t, testdbseed.DefaultProjectID)
	srv := newTestServer(t, func(d *Dependencies) {
		d.Core.Store = sessions
		d.Core.Projects = project.NewSQLRegistry(sessions.DB())
	})
	created, err := sessions.Sessions.Create(t.Context(), wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	for _, content := range []string{"one", "two", "three", "four", "five"} {
		testutil.FailErr(t, "append message", srv.Admin.SessionAdmin.Lifecycle.Sessions.Runner.Transcript.AppendPlain(
			t.Context(), created.ID, wire.Message{Role: wire.MessageRoleAssistant, Content: content}))
	}
	read := func(query string, wantStatus int) wire.SessionTranscriptPage {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?"+query, nil))
		if rec.Code != wantStatus {
			t.Fatalf("%s: status = %d body=%s", query, rec.Code, rec.Body.String())
		}
		var page wire.SessionTranscriptPage
		if wantStatus == http.StatusOK {
			testutil.FailErr(t, "decode "+query, json.Unmarshal(rec.Body.Bytes(), &page))
		}
		return page
	}
	contents := func(page wire.SessionTranscriptPage) string {
		out := make([]string, 0, len(page.Messages))
		for _, msg := range page.Messages {
			out = append(out, msg.Content)
		}
		return strings.Join(out, ",")
	}
	all := read("limit=10", http.StatusOK)
	ids := map[string]string{}
	for _, msg := range all.Messages {
		ids[msg.Content] = msg.ID
	}
	if all.OrdBounds == nil || all.OrdBounds.FirstOrd != all.Messages[0].Ord || all.OrdBounds.LastOrd != all.Messages[4].Ord {
		t.Fatalf("ord_bounds = %+v, want the span of all five rows", all.OrdBounds)
	}
	ords := map[string]int64{}
	for _, msg := range all.Messages {
		ords[msg.Content] = msg.Ord
	}
	cases := []struct{ query, want string }{
		{"limit=2", "four,five"},
		{"limit=2&before_message_id=" + ids["four"], "two,three"},
		{"limit=2&after_message_id=" + ids["two"], "three,four"},
		{"limit=2&before_ord=" + strconv.FormatInt(ords["four"], 10), "two,three"},
		{"limit=2&after_ord=" + strconv.FormatInt(ords["two"], 10), "three,four"},
		{"limit=2&after_ord=0", "one,two"},
		{"limit=2&from=oldest", "one,two"},
	}
	for _, tc := range cases {
		page := read(tc.query, http.StatusOK)
		if got := contents(page); got != tc.want {
			t.Fatalf("%s: messages = %s, want %s", tc.query, got, tc.want)
		}
		// Every window reports the whole transcript's span, not its own.
		if page.OrdBounds == nil || *page.OrdBounds != *all.OrdBounds {
			t.Fatalf("%s: ord_bounds = %+v, want %+v", tc.query, page.OrdBounds, all.OrdBounds)
		}
	}
	if oldest := read("limit=2&from=oldest", http.StatusOK); oldest.BeforeCursor != "" || oldest.AfterCursor == "" {
		t.Fatalf("oldest window cursors: before=%q after=%q", oldest.BeforeCursor, oldest.AfterCursor)
	}
	// An ordinal past the transcript opens an empty window that still reports the span.
	if beyond := read("after_ord="+strconv.FormatInt(ords["five"]+10, 10), http.StatusOK); len(beyond.Messages) != 0 || beyond.OrdBounds == nil {
		t.Fatalf("beyond window: messages=%d ord_bounds=%+v", len(beyond.Messages), beyond.OrdBounds)
	}
	read("before_message_id=2c0f6f8e-5d4b-4c55-9d7e-8f1b2a3c4d5e", http.StatusNotFound)
	read("from=oldest&after_message_id="+ids["two"], http.StatusBadRequest)
	read("after_ord=1&before_ord=3", http.StatusBadRequest)
	read("before_ord=0", http.StatusBadRequest)
	read("after_ord=next", http.StatusBadRequest)
	read("from=middle", http.StatusBadRequest)
}

func TestGetSessionMessagesFiltersReusedChildByWorkerJob(t *testing.T) {
	sessions := storetest.New(t, testdbseed.DefaultProjectID)
	srv := newTestServer(t, func(d *Dependencies) {
		d.Core.Store = sessions
		d.Core.Projects = project.NewSQLRegistry(sessions.DB())
	})
	created, err := sessions.Sessions.Create(t.Context(), wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	firstJobID := "11111111-1111-4111-8111-111111111111"
	secondJobID := "22222222-2222-4222-8222-222222222222"
	testdbseed.InsertWorkerJob(t, sessions.DB(), firstJobID, created.ID)
	testdbseed.InsertWorkerJob(t, sessions.DB(), secondJobID, created.ID)
	testutil.FailErr(t, "append worker messages", srv.Admin.SessionAdmin.Lifecycle.Sessions.Runner.Transcript.AppendPlain(
		t.Context(), created.ID,
		wire.Message{Role: wire.MessageRoleAssistant, Content: "first", WorkerID: firstJobID},
		wire.Message{Role: wire.MessageRoleAssistant, Content: "second", WorkerID: secondJobID},
	))

	req := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?worker_id="+secondJobID, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var page wire.SessionTranscriptPage
	testutil.FailErr(t, "decode worker page", json.Unmarshal(rec.Body.Bytes(), &page))
	if len(page.Messages) != 1 || page.Messages[0].Content != "second" || page.Messages[0].WorkerID != secondJobID {
		t.Fatalf("messages = %+v", page.Messages)
	}

	bad := newAuthedRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/messages?worker_id=not-a-uuid", nil)
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid worker_id status = %d", badRec.Code)
	}
}

func TestGetSessionMessagesNotFound(t *testing.T) {
	srv := newTestServer(t)

	req := newAuthedRequest(http.MethodGet, "/v1/sessions/nonexistent/messages", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}
