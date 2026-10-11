package hostcontracts

import (
	"encoding/json"
	"net/http"
	"testing"

	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestGetAgentPresenceReturnsTheProjectSnapshot(t *testing.T) {
	srv, _, _, p := contractfixture.NewPresenceServer(t)
	rec := contractfixture.GetAgentPresence(t, srv, p.ID)
	got := contractfixture.DecodeAgentPresence(t, rec)
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

	other := contractfixture.CreateProjectForTest(t, srv, t.TempDir())
	if empty := contractfixture.DecodeAgentPresence(t, contractfixture.GetAgentPresence(t, srv, other.ID)); empty.ProjectID != other.ID || empty.Sessions == nil || len(empty.Sessions) != 0 {
		t.Fatalf("other project snapshot = %+v", empty)
	}
}

func TestGetAgentPresenceUnknownProject(t *testing.T) {
	srv, _, _, _ := contractfixture.NewPresenceServer(t)
	contractfixture.AssertErrorResponse(t, contractfixture.GetAgentPresence(t, srv, "00000000-0000-4000-8000-000000000001"), http.StatusNotFound, "project_not_found")
}

// Only content changes re-check presence; a revision without content change leaves reads current.

func TestEditorDocumentChangeRechecksPresence(t *testing.T) {
	srv, tracker, _, p := contractfixture.NewPresenceServer(t)
	doc := &editordoc.Document{ID: "doc-a", ProjectID: p.ID, Revision: 5}

	srv.Routes.Activity.EditorDocumentChanged(t.Context(), editordoc.Change{Document: doc, ContentChanged: false})
	tracker.Settle()
	if read := contractfixture.DecodeAgentPresence(t, contractfixture.GetAgentPresence(t, srv, p.ID)).Sessions[0].Reads[0]; read.Stale {
		t.Fatalf("non-content change marked read stale: %+v", read)
	}

	srv.Routes.Activity.EditorDocumentChanged(t.Context(), editordoc.Change{Document: doc, ContentChanged: true})
	tracker.Settle()
	if read := contractfixture.DecodeAgentPresence(t, contractfixture.GetAgentPresence(t, srv, p.ID)).Sessions[0].Reads[0]; !read.Stale {
		t.Fatalf("content change left read current: %+v", read)
	}
}
