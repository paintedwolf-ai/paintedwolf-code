package editordoc

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

type editorFixture struct {
	externalFixture
	personID string
}

func newEditorFixture(t *testing.T, files map[string]string) editorFixture {
	t.Helper()
	return editorFixture{externalFixture: newExternalFixture(t, files)}
}

func (f *editorFixture) retain(t *testing.T, client string, documents ...*Document) {
	t.Helper()
	ids := make([]string, len(documents))
	for i, d := range documents {
		ids[i] = d.ID
	}
	testutil.FailErr(t, "retain tabs", f.service.ReplaceRetention(t.Context(), f.project.ID, client, ids, nil, nil))
	testutil.FailErr(t, "read tab owner", f.store.db.QueryRowContext(t.Context(),
		`SELECT person_id FROM editor_document_retention WHERE client_id=? LIMIT 1`, client).Scan(&f.personID))
}

// selectRange publishes the peer's selection from anchor to head, in UTF-16 offsets of its text.
func (f *editorFixture) selectRange(t *testing.T, peer *documentcore.Engine, joined *Document, client string, anchor, head uint32) {
	t.Helper()
	low, high := min(anchor, head), max(anchor, head)
	anchored, err := peer.Call(t.Context(), documentcore.Request{Action: "anchor", Handle: 1, OmitText: true,
		Edits: []documentcore.Edit{{Index: low, Delete: high - low}}})
	testutil.FailErr(t, "anchor peer selection", err)
	selected := PresenceRange{Anchor: anchored.Anchors[0].Start, Head: anchored.Anchors[0].End}
	if anchor > head {
		selected = PresenceRange{Anchor: selected.Head, Head: selected.Anchor}
	}
	testutil.FailErr(t, "publish selection", f.service.UpdatePresence(t.Context(), joined.ID, f.project.ID,
		Participant{ClientID: client, Incarnation: joined.Participants[0].Incarnation, Ranges: []PresenceRange{selected}}))
}

func (f *editorFixture) editor(t *testing.T, rootIDs ...string) []OpenDocument {
	t.Helper()
	if rootIDs == nil {
		rootIDs = []string{f.rootID}
	}
	open, err := f.service.PersonEditor(t.Context(), f.project.ID, "", f.personID, rootIDs)
	testutil.FailErr(t, "read person editor", err)
	return open
}

func TestPersonEditorListsRetainedDocumentsWithSelectionLines(t *testing.T) {
	f := newEditorFixture(t, map[string]string{"a.txt": "one\ntwo\nthree\nfour\n", "b.txt": "b\n", "c.txt": "c\n"})
	a, b, c := f.open(t, "a.txt"), f.open(t, "b.txt"), f.open(t, "c.txt")
	f.retain(t, "window:main", c, b, a)
	peer, joined := replicaForTest(t, f.externalFixture, a, "window:main")
	f.selectRange(t, peer, joined, "window:main", 14, 4)
	_, err := f.service.SubmitReplica(t.Context(), f.project.ID, a.ID, peerEdit(t, peer, joined, "window:main", documentcore.Edit{Insert: "zero\n"}))
	testutil.FailErr(t, "type above the selection", err)

	open := f.editor(t)
	if len(open) != 3 || open[0].Path != "a.txt" || open[1].Path != "b.txt" || open[2].Path != "c.txt" {
		t.Fatalf("open documents = %+v", open)
	}
	view := open[0].View
	if view == nil || view.StartLine != 3 || view.EndLine != 4 || view.Empty || !open[0].Unsaved {
		t.Fatalf("selected document = %+v view %+v; want unsaved, lines 3-4", open[0], view)
	}
	if open[1].View != nil || open[1].Unsaved {
		t.Fatalf("unviewed document = %+v", open[1])
	}
}

func TestPersonEditorReportsCursorAndNewestView(t *testing.T) {
	f := newEditorFixture(t, map[string]string{"a.txt": "one\ntwo\n", "b.txt": "x\ny\nz\n"})
	a, b := f.open(t, "a.txt"), f.open(t, "b.txt")
	f.retain(t, "window:main", a, b)
	peerA, joinedA := replicaForTest(t, f.externalFixture, a, "window:main")
	peerB, joinedB := replicaForTest(t, f.externalFixture, b, "window:main")
	f.selectRange(t, peerA, joinedA, "window:main", 0, 7)
	f.selectRange(t, peerB, joinedB, "window:main", 4, 4)

	open := f.editor(t)
	if len(open) != 2 || open[0].Path != "b.txt" || open[0].View == nil || !open[0].View.Empty || open[0].View.StartLine != 3 {
		t.Fatalf("newest view = %+v; want cursor on b.txt line 3", open)
	}
	if v := open[1].View; v == nil || v.StartLine != 1 || v.EndLine != 2 {
		t.Fatalf("older view = %+v; want lines 1-2", v)
	}
	testutil.FailErr(t, "heartbeat", f.service.UpdatePresence(t.Context(), a.ID, f.project.ID,
		Participant{ClientID: "window:main", Incarnation: joinedA.Participants[0].Incarnation, Ranges: f.rangesOf(t, a.ID)}))
	if open := f.editor(t); open[0].Path != "b.txt" {
		t.Fatalf("heartbeat reordered views: %+v", open)
	}
}

func TestPersonEditorScopesToPersonAndRoots(t *testing.T) {
	f := newEditorFixture(t, map[string]string{"a.txt": "one\ntwo\n"})
	a := f.open(t, "a.txt")
	f.retain(t, "window:main", a)
	peer, joined := replicaForTest(t, f.externalFixture, a, "window:main")
	f.selectRange(t, peer, joined, "window:main", 0, 3)
	other := f.service.participants[a.ID]["window:main"]
	other.ClientID, other.PersonID, other.SelectedAt = "window:other", uuid.NewString(), other.SelectedAt.Add(time.Hour)
	other.Ranges = []PresenceRange{{Anchor: other.Ranges[0].Head, Head: other.Ranges[0].Head}}
	f.service.participants[a.ID]["window:other"] = other
	if open := f.editor(t); len(open) != 1 || open[0].View == nil || open[0].View.Empty {
		t.Fatalf("sender's view = %+v; another person's newer cursor must not replace it", open)
	}
	if open := f.editor(t, uuid.NewString()); len(open) != 0 {
		t.Fatalf("documents outside the session roots = %+v", open)
	}
	f.personID = uuid.NewString()
	if open := f.editor(t); len(open) != 0 {
		t.Fatalf("another person's documents = %+v", open)
	}
}

func (f *editorFixture) rangesOf(t *testing.T, documentID string) []PresenceRange {
	t.Helper()
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, documentID)
	testutil.FailErr(t, "read presence", err)
	return current.Participants[0].Ranges
}
