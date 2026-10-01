package editordoc

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCommandHistorySeparatesPeerContextAndSurvivesReceiptReplay(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "AB"})
	d := f.open(t, "a.txt")
	in := agentEdit(d, "AagentB")
	agent, err := f.service.ApplyAgentEdit(t.Context(), in)
	testutil.FailErr(t, "apply agent contribution", err)
	window, joined := replicaForTest(t, f.externalFixture, agent.Document, "window")
	peer, other := replicaForTest(t, f.externalFixture, agent.Document, "peer")
	pending := peerEdit(t, peer, other, "peer", documentcore.Edit{Index: 3, Insert: "mine"})
	_, err = f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
	testutil.FailErr(t, "accept intervening participant", err)
	command := RevertChange{ClientID: "window", OperationID: uuid.NewString(), ChangeOperationID: in.OperationID, Epoch: joined.Epoch, HistoryVector: joined.StateVector}
	reverted, err := f.service.Revert(t.Context(), f.project.ID, d.ID, command)
	testutil.FailErr(t, "accept human undo", err)
	effect := reverted.CommandHistory
	if effect == nil || effect.OperationID != command.OperationID || effect.Epoch != joined.Epoch {
		t.Fatalf("missing exact command history: %+v", effect)
	}
	before, err := window.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: effect.BeforeUpdate})
	testutil.FailErr(t, "receive context independently", err)
	if before.Text != "AagmineentB" {
		t.Fatalf("context included human command: %q", before.Text)
	}
	after, err := window.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: effect.Update})
	testutil.FailErr(t, "receive human command", err)
	if after.Text != "AmineB" {
		t.Fatalf("command lost peer text: %q", after.Text)
	}
	later := peerEdit(t, window, joined, "window", documentcore.Edit{Index: 0, Insert: "later"})
	_, err = f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, later)
	testutil.FailErr(t, "accept later edit", err)
	testutil.FailErr(t, "stop service", f.service.Close(t.Context()))
	f.service = New(f.store, f.recorder, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, f.service)
	replay, err := f.service.Revert(t.Context(), f.project.ID, d.ID, command)
	testutil.FailErr(t, "replay accepted action", err)
	if replay.Draft != "laterAmineB" || replay.CommandHistory == nil || !bytes.Equal(replay.CommandHistory.Update, effect.Update) || !bytes.Equal(replay.CommandHistory.BeforeUpdate, effect.BeforeUpdate) {
		t.Fatalf("replay changed the command boundary: %+v", replay)
	}
}

func TestSnapshotCommandHistoryContainsOnlyTheRequestedTransition(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	window, joined := replicaForTest(t, f.externalFixture, d, "window")
	in := SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision, HistoryVector: joined.StateVector}, Content: "replacement", EOL: "lf"}
	result, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, in)
	testutil.FailErr(t, "replace draft", err)
	if result.CommandHistory == nil {
		t.Fatal("missing replacement history")
	}
	snapshot, err := window.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: result.CommandHistory.BeforeUpdate})
	testutil.FailErr(t, "apply dependency delta", err)
	if snapshot.Text != "base" {
		t.Fatalf("dependency changed text: %q", snapshot.Text)
	}
	snapshot, err = window.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: result.CommandHistory.Update})
	testutil.FailErr(t, "apply replacement delta", err)
	if snapshot.Text != "replacement" {
		t.Fatalf("replacement = %q", snapshot.Text)
	}
	replay, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, in)
	testutil.FailErr(t, "replay replacement", err)
	if replay.CommandHistory == nil || !bytes.Equal(replay.CommandHistory.Update, result.CommandHistory.Update) {
		t.Fatal("replacement receipt lost history")
	}
}

func TestResolvedMergeRetainsCommandHistoryAfterSavingAndReplay(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	window, joined := replicaForTest(t, f.externalFixture, d, "window")
	in := ConflictResolution{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision,
		DiskSHA256: d.BaseSHA256, Content: "merged", EOL: "lf", HistoryVector: joined.StateVector}
	resolved, err := f.service.Resolve(t.Context(), f.project, d.ID, in)
	testutil.FailErr(t, "resolve and save merge", err)
	if resolved.CommandHistory == nil {
		t.Fatal("saved merge lost command history")
	}
	_, err = window.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: resolved.CommandHistory.BeforeUpdate})
	testutil.FailErr(t, "receive merge context", err)
	snapshot, err := window.Call(t.Context(), documentcore.Request{Action: "apply", Handle: 1, Update: resolved.CommandHistory.Update})
	testutil.FailErr(t, "receive merge action", err)
	if snapshot.Text != "merged" || f.disk(t, "a.txt") != "merged" {
		t.Fatalf("merge was not applied and saved: %q", snapshot.Text)
	}
	replay, err := f.service.Resolve(t.Context(), f.project, d.ID, in)
	testutil.FailErr(t, "replay saved merge", err)
	if replay.CommandHistory == nil || !bytes.Equal(replay.CommandHistory.Update, resolved.CommandHistory.Update) {
		t.Fatal("merge receipt lost its exact command history")
	}
}
