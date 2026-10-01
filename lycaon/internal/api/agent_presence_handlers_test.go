package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type presenceTestChats map[string]agentpresence.ChatRef

func (c presenceTestChats) Chat(_ context.Context, sessionID string) (agentpresence.ChatRef, bool) {
	ref, ok := c[sessionID]
	return ref, ok
}

// presenceTestAnchors anchors every span and reports whether spans still hold.
type presenceTestAnchors struct{ hold bool }

func (a *presenceTestAnchors) AnchorSpans(_ context.Context, _, documentID string, revision int64, spans []agentpresence.Span) (agentpresence.Anchored, error) {
	out := agentpresence.Anchored{DocumentID: documentID, Epoch: 2, Revision: revision}
	for _, span := range spans {
		out.Spans = append(out.Spans, agentpresence.AnchoredSpan{
			Anchor: fmt.Appendf(nil, "anchor:%d", span.StartLine), Head: fmt.Appendf(nil, "head:%d", span.EndLine), Expected: "text", Checkable: true,
		})
	}
	return out, nil
}

func (a *presenceTestAnchors) AnchorPathSpans(context.Context, string, agentpresence.Target, []agentpresence.Span) (agentpresence.Anchored, bool, error) {
	return agentpresence.Anchored{}, false, nil
}

func (a *presenceTestAnchors) SpansHold(_ context.Context, _, _ string, spans []agentpresence.AnchoredSpan) ([]bool, error) {
	out := make([]bool, len(spans))
	for i := range out {
		out[i] = a.hold
	}
	return out, nil
}

// newPresenceServer installs a tracker with one read by chat-1 in the project.
func newPresenceServer(t *testing.T) (*Server, *agentpresence.Tracker, *presenceTestAnchors, wire.Project) {
	t.Helper()
	chats := presenceTestChats{}
	anchors := &presenceTestAnchors{hold: false}
	tracker := agentpresence.New(chats, nil)
	tracker.SetAnchors(anchors)
	srv := newTestServer(t, func(d *Dependencies) { d.AgentPresence = tracker })
	p := createProjectForTest(t, srv, t.TempDir())
	chats["chat-1"] = agentpresence.ChatRef{ProjectID: p.ID, SessionID: "chat-1", Title: "Config defaults"}
	tracker.ReadsReturned(t.Context(), agentpresence.Call{SessionID: "chat-1", ToolCallID: "call-1", Tool: "read"}, []agentpresence.Read{{
		Target:   agentpresence.Target{RootID: p.Roots[0].ID, Path: "pkg/a.go"},
		Document: agentpresence.Document{ID: "doc-a", Revision: 4},
		Extent:   wire.AgentPresenceExtentRange,
		Spans:    []agentpresence.Span{{StartLine: 3, EndLine: 9}},
	}})
	tracker.Settle()
	return srv, tracker, anchors, p
}

func getAgentPresence(t *testing.T, srv *Server, projectID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, newAuthedRequest(http.MethodGet, "/v1/projects/"+projectID+"/agent-presence", nil))
	return rec
}

func decodeAgentPresence(t *testing.T, rec *httptest.ResponseRecorder) wire.AgentPresenceSnapshot {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got wire.AgentPresenceSnapshot
	testutil.FailErr(t, "decode presence", json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

func TestGetAgentPresenceReturnsTheProjectSnapshot(t *testing.T) {
	srv, _, _, p := newPresenceServer(t)
	rec := getAgentPresence(t, srv, p.ID)
	got := decodeAgentPresence(t, rec)
	if got.ProjectID != p.ID || got.Revision < 1 || len(got.Sessions) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
	chat := got.Sessions[0]
	if chat.SessionID != "chat-1" || chat.Title != "Config defaults" || len(chat.Reads) != 1 {
		t.Fatalf("session = %+v", chat)
	}
	read := chat.Reads[0]
	if read.ToolCallID != "call-1" || read.Tool != "read" || read.RootID != p.Roots[0].ID || read.Path != "pkg/a.go" ||
		read.DocumentID != "doc-a" || read.Epoch != 2 || read.Revision != 4 || read.Extent != wire.AgentPresenceExtentRange || read.Stale {
		t.Fatalf("read = %+v", read)
	}
	if len(read.Ranges) != 1 || read.Ranges[0].StartLine != 3 || read.Ranges[0].EndLine != 9 || string(read.Ranges[0].Anchor) != "anchor:3" {
		t.Fatalf("ranges = %+v", read.Ranges)
	}

	// Collections are present on the wire even when empty.
	var raw struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	testutil.FailErr(t, "decode raw presence", json.Unmarshal(rec.Body.Bytes(), &raw))
	for _, key := range []string{"activities", "reads", "intents", "worker_drafts"} {
		if value, ok := raw.Sessions[0][key]; !ok || string(value) == "null" {
			t.Fatalf("session %q = %s", key, value)
		}
	}

	other := createProjectForTest(t, srv, t.TempDir())
	if empty := decodeAgentPresence(t, getAgentPresence(t, srv, other.ID)); empty.ProjectID != other.ID || empty.Sessions == nil || len(empty.Sessions) != 0 {
		t.Fatalf("other project snapshot = %+v", empty)
	}
}

func TestGetAgentPresenceUnknownProject(t *testing.T) {
	srv, _, _, _ := newPresenceServer(t)
	assertErrorResponse(t, getAgentPresence(t, srv, "00000000-0000-4000-8000-000000000001"), http.StatusNotFound, "project_not_found")
}

// Only content changes re-check presence; a revision without content change leaves reads current.
func TestEditorDocumentChangeRechecksPresence(t *testing.T) {
	srv, tracker, _, p := newPresenceServer(t)
	doc := &editordoc.Document{ID: "doc-a", ProjectID: p.ID, Revision: 5}

	srv.editorDocumentChanged(t.Context(), editordoc.Change{Document: doc, ContentChanged: false})
	tracker.Settle()
	if read := decodeAgentPresence(t, getAgentPresence(t, srv, p.ID)).Sessions[0].Reads[0]; read.Stale {
		t.Fatalf("non-content change marked read stale: %+v", read)
	}

	srv.editorDocumentChanged(t.Context(), editordoc.Change{Document: doc, ContentChanged: true})
	tracker.Settle()
	if read := decodeAgentPresence(t, getAgentPresence(t, srv, p.ID)).Sessions[0].Reads[0]; !read.Stale {
		t.Fatalf("content change left read current: %+v", read)
	}
}
