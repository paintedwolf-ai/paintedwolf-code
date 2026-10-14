package editordoc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWindowPresenceIdentityAndSelections(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "first", "b.txt": "second"})
	a, b := f.open(t, "a.txt"), f.open(t, "b.txt")
	_, first := replicaForTest(t, f, a, "first-window")
	_, second := replicaForTest(t, f, a, "second-window")
	firstNumber := first.Participants[0].WindowNumber
	if second.Participants[0].WindowNumber == second.Participants[1].WindowNumber {
		t.Fatal("window numbers collided")
	}
	_, otherFile := replicaForTest(t, f, b, "first-window")
	if otherFile.Participants[0].WindowNumber != firstNumber {
		t.Fatal("window identity changed across files")
	}
	in := Participant{ClientID: "first-window", Incarnation: first.Participants[0].Incarnation, WindowNumber: 999, Main: 1,
		Ranges: []PresenceRange{{Anchor: []byte{1}, Head: []byte{2}}, {Anchor: []byte{3}, Head: []byte{4}}}}
	testutil.FailErr(t, "publish selections", f.service.UpdatePresence(t.Context(), a.ID, f.project.ID, in))
	in.Ranges[0].Anchor[0] = 9
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, a.ID)
	testutil.FailErr(t, "read presence", err)
	person := current.Participants[0]
	if person.WindowNumber != firstNumber || person.Main != 1 || len(person.Ranges) != 2 || person.Ranges[0].Anchor[0] != 1 {
		t.Fatalf("presence projection = %+v", person)
	}
	testutil.FailErr(t, "leave first file", f.service.Leave(t.Context(), a.ID, f.project.ID, "first-window", first.Participants[0].Incarnation))
	_, rejoined := replicaForTest(t, f, a, "first-window")
	if rejoined.Participants[0].WindowNumber != firstNumber {
		t.Fatal("rejoining renumbered the window")
	}
	if err := f.service.UpdatePresence(t.Context(), a.ID, f.project.ID, Participant{ClientID: "first-window", Main: 1}); err == nil {
		t.Fatal("accepted a primary range outside the selection")
	}
}

func TestReopenedWindowRejectsStalePresenceAndDeparture(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "first"})
	d := f.open(t, "a.txt")
	first, err := f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: "window", Incarnation: "first", Epoch: 1})
	testutil.FailErr(t, "join first editor", err)
	second, err := f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: "window", Incarnation: "second", Epoch: 1})
	testutil.FailErr(t, "reopen editor", err)
	if first.Participants[0].WindowNumber != second.Participants[0].WindowNumber {
		t.Fatal("reopening changed the window number")
	}
	err = f.service.UpdatePresence(t.Context(), d.ID, f.project.ID, Participant{ClientID: "window", Incarnation: "first"})
	if !errors.Is(err, ErrReplicaIdentity) {
		t.Fatalf("stale presence error = %v", err)
	}
	testutil.FailErr(t, "leave old editor", f.service.Leave(t.Context(), d.ID, f.project.ID, "window", "first"))
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, d.ID)
	testutil.FailErr(t, "read current editor", err)
	if len(current.Participants) != 1 || current.Participants[0].Incarnation != "second" {
		t.Fatalf("old departure removed the reopened editor: %+v", current.Participants)
	}
	testutil.FailErr(t, "publish current editor", f.service.UpdatePresence(t.Context(), d.ID, f.project.ID, Participant{ClientID: "window", Incarnation: "second"}))
}

func TestPresenceExpiryNeverDiscardsDocumentWork(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	peer, joined := replicaForTest(t, f, d, "window")
	initial := joined.Participants[0].UpdatedAt
	accepted, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 0, Insert: "mine"}))
	testutil.FailErr(t, "accept text", err)
	changes := len(*f.changes)
	testutil.FailErr(t, "heartbeat", f.service.UpdatePresence(t.Context(), d.ID, f.project.ID, Participant{ClientID: "window", Incarnation: joined.Participants[0].Incarnation}))
	if len(*f.changes) != changes {
		t.Fatal("heartbeat emitted a document event")
	}
	f.service.expireParticipant(t.Context(), d.ID, "window", initial)
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, d.ID)
	testutil.FailErr(t, "read refreshed presence", err)
	if len(current.Participants) != 1 {
		t.Fatal("old expiration removed refreshed participant")
	}
	f.service.expireParticipant(t.Context(), d.ID, "window", current.Participants[0].UpdatedAt)
	current, err = f.service.CurrentSnapshot(t.Context(), f.project.ID, d.ID)
	testutil.FailErr(t, "read after expiration", err)
	if len(current.Participants) != 0 || current.Draft != accepted.Document.Draft || !current.Dirty || current.Revision != accepted.Document.Revision {
		t.Fatalf("presence expiration changed durable work: %+v", current)
	}
}

func TestReloadPreservesUnsentPeerTyping(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "one\ntwo\n"})
	before := f.open(t, "a.txt")
	peer, joined := replicaForTest(t, f, before, "window")
	pending := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 4, Insert: "my "})
	f.write(t, "a.txt", "ONE\ntwo\n")
	reloaded, err := f.service.Reload(t.Context(), f.project, before.ID, DocumentCommand{ClientID: "other", OperationID: uuid.NewString(), ExpectedRevision: before.Revision})
	testutil.FailErr(t, "reload disk", err)
	if reloaded.Epoch != before.Epoch {
		t.Fatal("reload replaced shared history")
	}
	accepted, err := f.service.SubmitReplica(t.Context(), f.project.ID, before.ID, pending)
	testutil.FailErr(t, "deliver concurrent typing", err)
	if accepted.Document.Draft != "ONE\nmy two\n" || !accepted.Document.Dirty {
		t.Fatalf("reload lost typing: %+v", accepted.Document)
	}
}

func TestFilesystemReplicaUsesExactSavedCheckpointAfterRestart(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	d := f.open(t, "a.txt")
	d, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "ONE\ntwo\nthree\n", EOL: "lf"})
	testutil.FailErr(t, "edit first line", err)
	pinned, err := f.service.Pin(t.Context(), f.project.ID, d.ID, d.Revision)
	testutil.FailErr(t, "pin save", err)
	_, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "ONE\ntwo\nmy three\n", EOL: "lf"})
	testutil.FailErr(t, "type after save snapshot", err)
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", uuid.NewString(), "", 0, pinned.Revision)
	testutil.FailErr(t, "publish earlier snapshot", err)
	testutil.FailErr(t, "stop service", f.service.Close(t.Context()))
	f.service = New(f.store, f.recorder, f.recorder.History, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, f.service)
	for _, outside := range []string{"ONE\nTWO\nthree\n", "FIRST\nTWO\nthree\n"} {
		f.write(t, "a.txt", outside)
		observed, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
		testutil.FailErr(t, "import outside save", err)
		expected := strings.Replace(outside, "three", "my three", 1)
		if observed.Draft != expected || observed.BaseContent != outside || !observed.Dirty || observed.Diverged || observed.Epoch != d.Epoch {
			t.Fatalf("saved checkpoint did not preserve concurrent text: %+v", observed)
		}
	}
}

// A save reserved against an older saved base has nothing left to publish:
// it is refused as stale, not recorded as a disk conflict, so a consistent
// document never reports divergence for it.
func TestSaveReservationBehindTheSavedBaseIsStaleNotDiverged(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\n"})
	d := f.open(t, "a.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "base\nmine\n", EOL: "lf"})
	testutil.FailErr(t, "type", err)
	pinned, err := f.service.PinSave(t.Context(), f.project.ID, d.ID, "window", uuid.NewString())
	testutil.FailErr(t, "reserve the save", err)
	// An agent lands and saves before the person's save publishes.
	landed, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(typed, "agent\nbase\nmine\n"))
	testutil.FailErr(t, "agent edit", err)
	if !landed.Saved {
		t.Fatalf("agent edit did not save: %+v", landed)
	}
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", uuid.NewString(), "", 0, pinned.Revision)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale reservation = %v, want ErrRevisionConflict", err)
	}
	current, err := f.store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "reload", err)
	if current.Diverged || current.Dirty || f.disk(t, "a.txt") != "agent\nbase\nmine\n" {
		t.Fatalf("stale reservation disturbed the document: %+v disk=%q", current, f.disk(t, "a.txt"))
	}
}

// An agent that inserts a line between two others leaves typing on the
// neighbouring line alone: the insertion anchors at its own line boundary.
func TestAgentInsertionBetweenLinesKeepsTypingOnTheNeighbouringLine(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "a\nb\n"})
	d := f.open(t, "a.txt")
	read, err := f.service.Pin(t.Context(), f.project.ID, d.ID, 0)
	testutil.FailErr(t, "agent read", err)
	_, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "ab\nb\n", EOL: "lf"})
	testutil.FailErr(t, "type on the first line", err)
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(read, "a\nc\nb\n"))
	testutil.FailErr(t, "insert a line between", err)
	if result.Document.Draft != "ab\nc\nb\n" {
		t.Fatalf("insertion moved the person's typing: %q", result.Document.Draft)
	}
}

// A document whose history no longer fits its budget starts a new epoch: the
// text and the unsaved draft carry over, replicas of the old epoch must
// synchronize again, and outside imports keep merging against the saved base.
func TestHistoryCapacityStartsANewEpochWithTheDraftIntact(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	d := f.open(t, "a.txt")
	peer, joined := replicaForTest(t, f, d, "window")
	accepted, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 5, Insert: "draft\n"}))
	testutil.FailErr(t, "type a draft", err)
	if accepted.Document.Draft != "base\ndraft\n" {
		t.Fatalf("draft = %q", accepted.Document.Draft)
	}

	unlock := f.service.docLocks.lock(documentLockKey(d.ID))
	err = f.service.rebaseEpoch(t.Context(), d.ID)
	unlock()
	testutil.FailErr(t, "start a new epoch", err)

	rebased, err := f.store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "reload", err)
	head, err := f.store.replicaHead(t.Context(), d.ID)
	testutil.FailErr(t, "reload head", err)
	if head.Epoch != 2 || rebased.Draft != "base\ndraft\n" || rebased.BaseContent != "base\n" || !rebased.Dirty {
		t.Fatalf("epoch rebase changed the text: epoch=%d %+v", head.Epoch, rebased)
	}
	// The old epoch's replica cannot integrate; it synchronizes into the new one.
	stale := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 0, Insert: "late "})
	if _, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, stale); !errors.Is(err, ErrReplicaEpoch) {
		t.Fatalf("old epoch update = %v, want ErrReplicaEpoch", err)
	}
	rejoined, err := f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: "window", Incarnation: uuid.NewString(), Epoch: 1})
	testutil.FailErr(t, "rejoin", err)
	if rejoined.Epoch != 2 || len(rejoined.CRDTUpdate) == 0 {
		t.Fatalf("rejoin did not answer the new epoch in full: %+v", rejoined)
	}
	fresh, err := documentcore.New(t.Context())
	testutil.FailErr(t, "peer engine", err)
	t.Cleanup(func() { testutil.FailErr(t, "close peer engine", fresh.Close(context.Background())) })
	_, err = fresh.Call(t.Context(), documentcore.Request{Action: "open", Handle: 1, Client: rejoined.ReplicaID, Update: rejoined.CRDTUpdate})
	testutil.FailErr(t, "hydrate rejoined peer", err)
	typed, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, peerEdit(t, fresh, rejoined, "window", documentcore.Edit{Index: 0, Insert: "new "}))
	testutil.FailErr(t, "type in the new epoch", err)
	if typed.Document.Draft != "new base\ndraft\n" {
		t.Fatalf("new epoch draft = %q", typed.Document.Draft)
	}
	// The saved base still anchors outside imports.
	f.write(t, "a.txt", "BASE\n")
	imported, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
	testutil.FailErr(t, "import after rebase", err)
	if imported.Draft != "new BASE\ndraft\n" || imported.BaseContent != "BASE\n" {
		t.Fatalf("import after rebase = %+v", imported)
	}
}
