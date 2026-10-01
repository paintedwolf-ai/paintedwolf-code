//go:build integration

package editordoc

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLongEditingSessionCompactsWithoutReplacingHistoryIdentity(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": strings.Repeat("a", 1<<20)})
	d := f.open(t, "a.txt")
	epoch := d.Epoch
	var latest string
	for iteration := 0; iteration < 12; iteration++ {
		latest = uuid.NewString()
		updated, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: latest, ExpectedRevision: d.Revision}, Content: strings.Repeat(string(rune('b'+iteration)), 1<<20), EOL: "lf"})
		testutil.FailErr(t, "replace full document", err)
		if updated.Epoch != epoch {
			t.Fatal("compaction changed document identity")
		}
		d = updated
	}
	head, err := f.store.replicaHead(t.Context(), d.ID)
	testutil.FailErr(t, "read compacted head", err)
	if len(head.Checkpoint) > replicaHistoryBytes/2 {
		t.Fatalf("checkpoint still holds old text: %d bytes", len(head.Checkpoint))
	}
	testutil.FailErr(t, "restart", f.service.Close(t.Context()))
	f.service = New(f.store, f.service.ledger, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, f.service)
	undone, err := f.service.Revert(t.Context(), f.project.ID, d.ID, RevertChange{ClientID: "window", OperationID: uuid.NewString(), ChangeOperationID: latest, Epoch: epoch})
	testutil.FailErr(t, "undo after compaction and restart", err)
	if undone.Draft != strings.Repeat("l", 1<<20) {
		t.Fatal("latest edit was not undoable after compaction")
	}
}

func TestFilesystemBranchCompactsWithoutSavingTheDraft(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": strings.Repeat("a", 1<<20) + "\n"})
	d := f.open(t, "a.txt")
	peer, joined := replicaForTest(t, f, d, "window")
	pending := peerEdit(t, peer, joined, "window", documentcore.Edit{Index: (1 << 20) + 1, Insert: "human draft\n"})
	_, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, pending)
	testutil.FailErr(t, "type draft", err)
	for i := 0; i < 12; i++ {
		f.write(t, "a.txt", strings.Repeat(string(rune('b'+i)), 1<<20)+"\n")
		d, err = f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
		testutil.FailErr(t, "import rewritten file", err)
		if !d.Dirty || !strings.HasSuffix(d.Draft, "human draft\n") {
			t.Fatal("outside rewrite lost unsaved typing")
		}
	}
	head, err := f.store.replicaHead(t.Context(), d.ID)
	testutil.FailErr(t, "read saved branch", err)
	if len(head.PublishedCheckpoint) > replicaHistoryBytes/2 {
		t.Fatalf("saved branch retained %d bytes", len(head.PublishedCheckpoint))
	}
	testutil.FailErr(t, "restart", f.service.Close(t.Context()))
	f.service = New(f.store, f.service.ledger, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, f.service)
	f.write(t, "a.txt", "final\n")
	d, err = f.service.ObserveDisk(t.Context(), f.project, d.ID, "window")
	testutil.FailErr(t, "import after restart", err)
	if d.Draft != "final\nhuman draft\n" {
		t.Fatalf("compacted saved branch lost convergence: %q", d.Draft)
	}
}

func TestTypingBatchesDoNotConsumeSemanticUndoRetention(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	d := f.open(t, "a.txt")
	edit := agentEdit(d, "agent\nbase\n")
	result, err := f.service.ApplyAgentEdit(t.Context(), edit)
	testutil.FailErr(t, "agent edit", err)
	peer, joined := replicaForTest(t, f, result.Document, "window")
	for i := 0; i < 270; i++ {
		_, err := f.service.SubmitReplica(t.Context(), f.project.ID, d.ID, peerEdit(t, peer, joined, "window", documentcore.Edit{Index: 11, Insert: "x"}))
		testutil.FailErr(t, "typing batch", err)
	}
	var count int
	testutil.FailErr(t, "count semantic undo", f.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM editor_document_changes WHERE document_id=?`, d.ID).Scan(&count))
	if count != 1 {
		t.Fatalf("typing added semantic undo rows: %d", count)
	}
	retained, _, err := f.service.retainedUndo(t.Context(), d.ID, &documentcore.Snapshot{})
	testutil.FailErr(t, "retain agent undo", err)
	if len(retained) != 1 {
		t.Fatalf("agent undo was crowded out: %d", len(retained))
	}
	undone, err := f.service.Revert(t.Context(), f.project.ID, d.ID, RevertChange{ClientID: "window", OperationID: uuid.NewString(), ChangeOperationID: edit.OperationID, Epoch: d.Epoch})
	testutil.FailErr(t, "undo agent after typing", err)
	if undone.Draft != "base\n"+strings.Repeat("x", 270) {
		t.Fatalf("selective undo lost later typing: %q", undone.Draft)
	}
}
