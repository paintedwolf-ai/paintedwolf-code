package editordoc

import (
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

// selectRange publishes the window's selection from anchor to head, in UTF-16 offsets of its text.
func selectRange(t *testing.T, f externalFixture, peer *documentcore.Engine, joined *Document, client string, anchor, head uint32) {
	t.Helper()
	low, high := min(anchor, head), max(anchor, head)
	anchored, err := peer.Call(t.Context(), documentcore.Request{Action: "anchor", Handle: 1, OmitText: true,
		Edits: []documentcore.Edit{{Index: low, Delete: high - low}}})
	testutil.FailErr(t, "anchor window selection", err)
	selected := PresenceRange{Anchor: anchored.Anchors[0].Start, Head: anchored.Anchors[0].End}
	if anchor > head {
		selected = PresenceRange{Anchor: selected.Head, Head: selected.Anchor}
	}
	testutil.FailErr(t, "publish selection", f.service.UpdatePresence(t.Context(), joined.ID, f.project.ID,
		Participant{ClientID: client, Incarnation: joined.Participants[0].Incarnation, Ranges: []PresenceRange{selected}}))
}

func visible(t *testing.T, f externalFixture, personID string, shown ...EditorShown) []VisibleDocument {
	t.Helper()
	out, err := f.service.VisibleDocuments(t.Context(), f.project.ID, "", personID, shown)
	testutil.FailErr(t, "read visible documents", err)
	return out
}

func TestVisibleDocumentsCarryEachWindowsOwnSelection(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "one\ntwo\nthree\nfour\n", "b.txt": "x\ny\nz\n"})
	a, b := f.open(t, "a.txt"), f.open(t, "b.txt")
	peerA, joinedA := replicaForTest(t, f, a, "window:main")
	peerB, joinedB := replicaForTest(t, f, b, "window:side")
	person := joinedA.Participants[0].PersonID
	selectRange(t, f, peerA, joinedA, "window:main", 14, 4)
	selectRange(t, f, peerB, joinedB, "window:side", 4, 4)
	_, err := f.service.SubmitReplica(t.Context(), f.project.ID, a.ID, peerEdit(t, peerA, joinedA, "window:main", documentcore.Edit{Insert: "zero\n"}))
	testutil.FailErr(t, "type above the selection", err)

	got := visible(t, f, person, EditorShown{ClientID: "window:side", DocumentID: b.ID}, EditorShown{ClientID: "window:main", DocumentID: a.ID})
	if len(got) != 2 || got[0].Path != "b.txt" || got[1].Path != "a.txt" {
		t.Fatalf("visible documents = %+v; want the reported order", got)
	}
	if s := got[0].Selection; s == nil || !s.Empty || s.StartLine != 3 {
		t.Fatalf("side window = %+v; want a cursor on line 3", s)
	}
	if s := got[1].Selection; s == nil || s.Empty || s.StartLine != 3 || s.EndLine != 4 || !got[1].Unsaved {
		t.Fatalf("main window = %+v %+v; want unsaved, lines 3-4", got[1], s)
	}
}

func TestVisibleDocumentsTakeNoSelectionFromAnotherWindowOrPerson(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "one\ntwo\n"})
	a := f.open(t, "a.txt")
	peer, joined := replicaForTest(t, f, a, "window:main")
	person := joined.Participants[0].PersonID
	selectRange(t, f, peer, joined, "window:main", 0, 3)

	if got := visible(t, f, person, EditorShown{ClientID: "window:other", DocumentID: a.ID}); len(got) != 1 || got[0].Selection != nil {
		t.Fatalf("a window without presence borrowed a selection: %+v", got)
	}
	if got := visible(t, f, uuid.NewString(), EditorShown{ClientID: "window:main", DocumentID: a.ID}); len(got) != 1 || got[0].Selection != nil {
		t.Fatalf("another person's selection was reported: %+v", got)
	}
}

func TestVisibleDocumentsSkipDocumentsOutsideTheProject(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "a\n"})
	f.open(t, "a.txt")
	if got := visible(t, f, uuid.NewString(), EditorShown{ClientID: "window:main", DocumentID: uuid.NewString()}); len(got) != 0 {
		t.Fatalf("unknown document reported: %+v", got)
	}
}
