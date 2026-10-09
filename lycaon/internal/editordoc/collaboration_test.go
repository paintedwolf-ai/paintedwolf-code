package editordoc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

func replicaForTest(t *testing.T, f externalFixture, d *Document, client string) (*documentcore.Engine, *Document) {
	t.Helper()
	joined, err := f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: client, Incarnation: uuid.NewString(), Epoch: 1})
	testutil.FailErr(t, "join editor replica", err)
	engine, err := documentcore.New(t.Context())
	testutil.FailErr(t, "create peer engine", err)
	t.Cleanup(func() { testutil.FailErr(t, "close peer engine", engine.Close(context.Background())) })
	_, err = engine.Call(t.Context(), documentcore.Request{Action: "open", Handle: 1, Client: joined.ReplicaID, Update: joined.CRDTUpdate})
	testutil.FailErr(t, "hydrate peer", err)
	return engine, joined
}

func peerEdit(t *testing.T, peer *documentcore.Engine, d *Document, client string, edits ...documentcore.Edit) ReplicaSubmission {
	t.Helper()
	change, err := peer.Call(t.Context(), documentcore.Request{Action: "edit", Handle: 1, Edits: edits})
	testutil.FailErr(t, "type in peer", err)
	return ReplicaSubmission{ClientID: client, ReplicaID: d.ReplicaID, OperationID: uuid.NewString(), Epoch: d.Epoch, Update: change.Update, Vector: change.Vector}
}

func TestReplicaAcceptanceIsDurableAndIdempotent(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "a🐺z"})
	d := f.open(t, "a.txt")
	left, l := replicaForTest(t, f, d, "left")
	right, r := replicaForTest(t, f, d, "right")
	first := peerEdit(t, left, l, "left", documentcore.Edit{Index: 1, Insert: "left"})
	second := peerEdit(t, right, r, "right", documentcore.Edit{Index: 3, Insert: "right"})
	a, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, first)
	testutil.FailErr(t, "accept first peer", err)
	b, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, second)
	testutil.FailErr(t, "accept second peer", err)
	if b.Document.Draft != "aleft🐺rightz" {
		t.Fatalf("merged text = %q", b.Document.Draft)
	}
	f.service = New(f.store, f.service.ledger, f.service.history, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, f.service)
	first.Vector = b.Document.StateVector
	replay, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, first)
	testutil.FailErr(t, "replay after restart with a newer vector", err)
	if replay.AcceptedRevision != a.AcceptedRevision || replay.Document.Revision != b.Document.Revision || replay.Document.Draft != b.Document.Draft {
		t.Fatalf("replayed receipt changed: %+v", replay)
	}
	first.Update = second.Update
	_, err = f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, first)
	if !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("different retry = %v", err)
	}
}

func TestLatePeerUpdateConvergesWithOutsideImport(t *testing.T) {
	for _, pendingFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "outside first", true: "typing first"}[pendingFirst], func(t *testing.T) {
			f := newExternalFixture(t, map[string]string{"a.txt": "one\ntwo\n"})
			d := f.open(t, "a.txt")
			peer, joined := replicaForTest(t, f, d, "window")
			pending := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 4, Insert: "my "})
			if pendingFirst {
				_, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
				testutil.FailErr(t, "accept typing", err)
			}
			f.write(t, "a.txt", "ONE\ntwo\n")
			imported, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
			testutil.FailErr(t, "observe outside version", err)
			accepted, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
			testutil.FailErr(t, "deliver or retry typing", err)
			if imported.Epoch != d.Epoch || accepted.Document.Draft != "ONE\nmy two\n" || accepted.Document.Diverged || !accepted.Document.Dirty {
				t.Fatalf("outside save did not converge: %+v", accepted.Document)
			}
			snapshot, err := peer.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: accepted.Document.CRDTUpdate})
			testutil.FailErr(t, "synchronize typing window", err)
			if snapshot.Text != accepted.Document.Draft {
				t.Fatalf("peer text = %q", snapshot.Text)
			}
		})
	}
}

func TestOutsideReplacementKeepsItsTextAcrossConcurrentDeletion(t *testing.T) {
	for _, pendingFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "outside first", true: "typing first"}[pendingFirst], func(t *testing.T) {
			const base = "updated by a source action\n"
			f := newExternalFixture(t, map[string]string{"a.txt": base})
			d := f.open(t, "a.txt")
			peer, joined := replicaForTest(t, f, d, "window")
			pending := peerEdit(t, peer, joined, "window", documentcore.Edit{Delete: uint32(len(base)), Insert: "unsaved local work\n"})
			if pendingFirst {
				_, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
				testutil.FailErr(t, "accept typing", err)
			}
			f.write(t, "a.txt", "another disk update\n")
			_, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
			testutil.FailErr(t, "observe outside replacement", err)
			accepted, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
			testutil.FailErr(t, "deliver typing", err)
			if !strings.Contains(accepted.Document.Draft, "another disk update") || !strings.Contains(accepted.Document.Draft, "unsaved local work") {
				t.Fatalf("replacement text fragmented: %q", accepted.Document.Draft)
			}
			snapshot, err := peer.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: accepted.Document.CRDTUpdate})
			testutil.FailErr(t, "synchronize peer", err)
			if snapshot.Text != accepted.Document.Draft {
				t.Fatalf("peer text = %q, host = %q", snapshot.Text, accepted.Document.Draft)
			}
		})
	}
}

func TestOutsideReplacementPreservesIndependentEditOnSameLine(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "x=1; y=1\n"})
	d := f.open(t, "a.txt")
	peer, joined := replicaForTest(t, f, d, "window")
	pending := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 2, Delete: 1, Insert: "2"})
	f.write(t, "a.txt", "x=1; y=2\n")
	_, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
	testutil.FailErr(t, "observe outside replacement", err)
	accepted, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
	testutil.FailErr(t, "deliver typing", err)
	if accepted.Document.Draft != "x=2; y=2\n" {
		t.Fatalf("independent changes did not merge: %q", accepted.Document.Draft)
	}
}

func TestAgentAnchorsFollowNonOverlappingPeerEdits(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	d := f.open(t, "a.txt")
	pinned, err := f.service.Pin(t.Context(), f.project.ID, d.ID, d.Revision)
	testutil.FailErr(t, "pin agent read", err)
	_, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "prefix\none\ntwo\nthree\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type before agent range", err)
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(pinned, "one\nTWO\nthree\n"))
	testutil.FailErr(t, "apply anchored agent edit", err)
	if result.Document.Draft != "prefix\none\nTWO\nthree\n" {
		t.Fatalf("anchored result = %q", result.Document.Draft)
	}
}

func TestAgentAnchorsRejectChangedSourceLine(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "const fooBar = 1;\n"})
	d := f.open(t, "a.txt")
	pinned, err := f.service.Pin(t.Context(), f.project.ID, d.ID, d.Revision)
	testutil.FailErr(t, "pin agent read", err)
	_, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "const fooBaz = 1;\n", EOL: "lf"})
	testutil.FailErr(t, "rename identifier", err)
	_, err = f.service.ApplyAgentEdit(t.Context(), agentEdit(pinned, "const fetchBar = 1;\n"))
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale source line accepted: %v", err)
	}
	current, err := f.service.store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "read retained typing", err)
	if current.Draft != "const fooBaz = 1;\n" || f.disk(t, "a.txt") != "const fooBar = 1;\n" {
		t.Fatalf("rejection changed document: %+v", current)
	}
}

func TestPinnedSaveLeavesLaterAcceptedEditsDirty(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	d, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "first", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type first version", err)
	pinned, err := f.service.Pin(t.Context(), f.project.ID, d.ID, d.Revision)
	testutil.FailErr(t, "pin save", err)
	_, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "second", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "continue typing", err)
	saved, err := f.service.Save(t.Context(), f.project, d.ID, "window", uuid.NewString(), "", 0, pinned.Revision)
	testutil.FailErr(t, "save frozen version", err)
	if f.disk(t, "a.txt") != "first" || saved.Draft != "second" || !saved.Dirty || saved.BaseContent != "first" {
		t.Fatalf("save replaced later edits: %+v, disk %q", saved, f.disk(t, "a.txt"))
	}
}

func TestFilesystemClockSurvivesOlderPinnedPublication(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "A"})
	d := f.open(t, "a.txt")
	d, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "saved", EOL: "lf"})
	testutil.FailErr(t, "type pinned version", err)
	pinned, err := f.service.Pin(t.Context(), f.project.ID, d.ID, d.Revision)
	testutil.FailErr(t, "pin publication", err)
	for _, outside := range []string{"B", "A"} {
		f.write(t, "a.txt", outside)
		_, err = f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
		testutil.FailErr(t, "import outside version", err)
	}
	_, err = f.service.Save(t.Context(), f.project, d.ID, "window", uuid.NewString(), "", 0, pinned.Revision)
	testutil.FailErr(t, "publish older checkpoint", err)
	f.write(t, "a.txt", "C")
	imported, err := f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
	testutil.FailErr(t, "import after rewound disk checkpoint", err)
	if !strings.Contains(imported.Draft, "C") || imported.BaseContent != "C" {
		t.Fatalf("outside update reused an accepted clock: %+v", imported)
	}
}

func TestConflictResolutionBindsBothReviewedVersions(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	in := ConflictResolution{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision, DiskSHA256: d.BaseSHA256, Content: "merged", EOL: "lf"}
	in.DiskSHA256 = "different"
	_, err := f.service.Resolve(t.Context(), f.project, d.ID, in)
	if !errors.Is(err, project.ErrSourceWriteConflict) {
		t.Fatalf("changed disk = %v", err)
	}
	in.DiskSHA256 = d.BaseSHA256
	in.ExpectedRevision++
	_, err = f.service.Resolve(t.Context(), f.project, d.ID, in)
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("changed document = %v", err)
	}
	in.ExpectedRevision = d.Revision
	resolved, err := f.service.Resolve(t.Context(), f.project, d.ID, in)
	testutil.FailErr(t, "resolve reviewed versions", err)
	replayed, err := f.service.Resolve(t.Context(), f.project, d.ID, in)
	testutil.FailErr(t, "retry resolved versions", err)
	if resolved.Draft != "merged" || replayed.Revision != resolved.Revision || f.disk(t, "a.txt") != "merged" {
		t.Fatalf("resolve replay changed result: %+v / %+v", resolved, replayed)
	}
}

func TestAgentChangeCanBeSelectivelyUndoneAfterRestart(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "A B"})
	d := f.open(t, "a.txt")
	in := agentEdit(d, "A agentB")
	result, err := f.service.ApplyAgentEdit(t.Context(), in)
	testutil.FailErr(t, "apply agent contribution", err)
	peer, joined := replicaForTest(t, f.externalFixture, result.Document, "window")
	pending := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 4, Insert: "mine"})
	_, err = f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
	testutil.FailErr(t, "accept contribution inside agent text", err)
	testutil.FailErr(t, "stop document service", f.service.Close(t.Context()))
	f.service = New(f.store, f.recorder, f.recorder.History, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, f.service)
	changes, err := f.service.Changes(t.Context(), f.project.ID, d.ID, 0, 100)
	testutil.FailErr(t, "read semantic history", err)
	if len(changes) != 1 || changes[0].OperationID != in.OperationID || changes[0].ActorKind != "agent" {
		t.Fatalf("semantic history = %+v", changes)
	}
	revert := RevertChange{ClientID: "window", OperationID: uuid.NewString(), ChangeOperationID: in.OperationID, Epoch: joined.Epoch}
	undone, err := f.service.Revert(t.Context(), f.project.ID, d.ID, revert)
	testutil.FailErr(t, "selectively undo agent contribution", err)
	if undone.Draft != "A mineB" || !undone.Dirty {
		t.Fatalf("undo = %+v", undone)
	}
	replay, err := f.service.Revert(t.Context(), f.project.ID, d.ID, revert)
	testutil.FailErr(t, "retry selective undo", err)
	if replay.Revision != undone.Revision {
		t.Fatal("retry created another undo")
	}
	redo, err := f.service.Revert(t.Context(), f.project.ID, d.ID, RevertChange{ClientID: "window", OperationID: uuid.NewString(), ChangeOperationID: revert.OperationID, Epoch: joined.Epoch})
	testutil.FailErr(t, "undo the undo", err)
	if redo.Draft != "A agmineentB" {
		t.Fatalf("redo = %q", redo.Draft)
	}
}

func TestAgentRetryAfterRestartDoesNotReapplyOrOverwritePeerText(t *testing.T) {
	for _, diverged := range []bool{false, true} {
		t.Run(map[bool]string{false: "published", true: "held"}[diverged], func(t *testing.T) {
			f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo"})
			d := f.open(t, "a.txt")
			d, err := f.service.Pin(t.Context(), f.project.ID, d.ID, d.Revision)
			testutil.FailErr(t, "pin agent input", err)
			in := agentEdit(d, "one\nTWO")
			if diverged {
				_, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "prefix\none\ntwo", EOL: "lf", MixedEOL: false})
				testutil.FailErr(t, "type before outside change", err)
				testutil.FailErr(t, "write outside text", os.WriteFile(filepath.Join(f.root, "a.txt"), []byte{0x63, 0x61, 0x66, 0xe9}, 0o644))
				_, err = f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
				testutil.FailErr(t, "observe outside text", err)
			}
			accepted, err := f.service.ApplyAgentEdit(t.Context(), in)
			testutil.FailErr(t, "accept agent edit", err)
			peer, joined := replicaForTest(t, f.externalFixture, accepted.Document, "window")
			update := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 0, Insert: "mine "})
			current, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, update)
			testutil.FailErr(t, "type after agent edit", err)
			testutil.FailErr(t, "close document service", f.service.Close(t.Context()))
			f.service = New(f.store, f.recorder, f.recorder.History, fixedRoots{p: f.project})
			closeServiceAtCleanup(t, f.service)
			replay, err := f.service.ApplyAgentEdit(t.Context(), in)
			testutil.FailErr(t, "retry accepted edit after restart", err)
			if replay.Document.Revision != current.Document.Revision || replay.Document.Draft != current.Document.Draft || replay.Saved != accepted.Saved || replay.HeldVersionID != accepted.HeldVersionID {
				t.Fatalf("retry changed accepted result: %+v / %+v", accepted, replay)
			}
			in.Content = "different"
			_, err = f.service.ApplyAgentEdit(t.Context(), in)
			if !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("different retry = %v", err)
			}
		})
	}
}

func TestPinnedSnapshotKeepsItsOwnMetadataAfterPublication(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base\r\n"})
	d := f.open(t, "a.txt")
	edited, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "edited\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "edit line ending and text", err)
	pinned, err := f.service.Pin(t.Context(), f.project.ID, d.ID, edited.Revision)
	testutil.FailErr(t, "pin metadata", err)
	saved, err := f.service.Save(t.Context(), f.project, d.ID, "window", uuid.NewString(), "", 0, pinned.Revision)
	testutil.FailErr(t, "publish pinned document", err)
	old, err := f.service.pinnedDocument(t.Context(), saved, pinned.Revision)
	testutil.FailErr(t, "read old metadata", err)
	if old.BaseContent != pinned.BaseContent || old.BaseEOL != pinned.BaseEOL || old.EOL != pinned.EOL || old.Dirty != pinned.Dirty || old.PublishedRevision != pinned.PublishedRevision || !bytes.Equal(old.StateVector, pinned.StateVector) {
		t.Fatalf("old snapshot borrowed current metadata: %+v / %+v", old, pinned)
	}
}

func TestMalformedReplicaIdentityCannotAliasAnAssignedPeer(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	peer, joined := replicaForTest(t, f, d, "window")
	valid := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 0, Insert: "mine"})
	if len(valid.Update) < 4 || valid.Update[0] != 1 || valid.Update[1] != 1 {
		t.Fatalf("unexpected fixture update prefix: %v", valid.Update)
	}
	id, n := binary.Uvarint(valid.Update[2:])
	if n <= 0 || id != uint64(joined.ReplicaID) {
		t.Fatalf("fixture identity = %d (%d)", id, n)
	}
	malformed := valid
	forged := binary.AppendUvarint(append([]byte{}, valid.Update[:2]...), id|(1<<53))
	forged = append(forged, valid.Update[2+n:]...)
	malformed.Update = forged
	_, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, malformed)
	if err == nil {
		t.Fatal("accepted a replica identity with masked high bits")
	}
	accepted, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, valid)
	testutil.FailErr(t, "reload accepted head after rejected input", err)
	if accepted.Document.Draft != "minebase" || accepted.Document.Revision != d.Revision+1 {
		t.Fatalf("rejected input changed head: %+v", accepted.Document)
	}
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
