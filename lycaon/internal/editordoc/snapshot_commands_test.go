package editordoc

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSnapshotCommandRetryNeverDiscardsLaterTyping(t *testing.T) {
	for _, action := range []string{"replace", "discard", "reload"} {
		t.Run(action, func(t *testing.T) {
			f := newAgentFixture(t, map[string]string{"a.txt": "base"})
			d := f.open(t, "a.txt")
			in := DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}
			run := func() (*Document, error) {
				switch action {
				case "replace":
					return f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: in, Content: "base", EOL: "crlf"})
				case "discard":
					return f.service.Discard(t.Context(), d.ID, f.project.ID, in)
				default:
					return f.service.Reload(t.Context(), f.project, d.ID, in)
				}
			}
			accepted, err := run()
			testutil.FailErr(t, "accept command", err)
			peer, joined := replicaForTest(t, f.externalFixture, accepted, "window")
			pending := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 4, Insert: " later"})
			latest, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
			testutil.FailErr(t, "accept later typing", err)
			testutil.FailErr(t, "stop command service", f.service.Close(t.Context()))
			f.service = New(f.store, f.recorder, f.recorder.History, fixedRoots{p: f.project})
			closeServiceAtCleanup(t, f.service)
			replayed, err := run()
			testutil.FailErr(t, "replay exact command", err)
			if replayed.Revision != latest.Document.Revision || replayed.Draft != "base later" {
				t.Fatalf("replay overwrote later text: %+v", replayed)
			}
			in.ExpectedRevision++
			_, err = run()
			if !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("changed retry = %v", err)
			}
		})
	}
}

func TestWindowOwnedUndoSurvivesHostRestart(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "ab"})
	d := f.open(t, "a.txt")
	left, joined := replicaForTest(t, f.externalFixture, d, "left")
	change, err := left.Call(t.Context(), documentcore.Request{Action: "edit", Handle: 1, Edits: []documentcore.Edit{{Index: 1, Insert: "left"}}, CaptureUndo: true})
	testutil.FailErr(t, "capture window undo", err)
	first := ReplicaSubmission{ClientID: "left", ReplicaID: joined.ReplicaID, OperationID: uuid.NewString(), Epoch: joined.Epoch, Update: change.Update, Vector: change.Vector}
	accepted, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, first)
	testutil.FailErr(t, "accept left contribution", err)
	right, other := replicaForTest(t, f.externalFixture, accepted.Document, "right")
	second := peerEdit(t, right, other, "right", documentcore.Edit{Index: 3, Insert: "right"})
	_, err = f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, second)
	testutil.FailErr(t, "accept right contribution", err)
	testutil.FailErr(t, "stop service", f.service.Close(t.Context()))
	restarted := New(f.store, f.recorder, f.recorder.History, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, restarted)
	_, err = left.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: second.Update})
	testutil.FailErr(t, "receive other window", err)
	undo, err := left.Call(t.Context(), documentcore.Request{Action: "undo", Handle: 1, Undo: change.Undo})
	testutil.FailErr(t, "undo in originating window", err)
	undone, err := restarted.SubmitReplica(t.Context(), f.project.ID, d.ID, ReplicaSubmission{ClientID: "left", ReplicaID: joined.ReplicaID, OperationID: uuid.NewString(), Epoch: joined.Epoch, Update: undo.Update, Vector: undo.Vector})
	testutil.FailErr(t, "deliver window undo after restart", err)
	if undone.Document.Draft != "arightb" {
		t.Fatalf("window undo lost another participant: %q", undone.Document.Draft)
	}
}
