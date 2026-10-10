package editordoc

import (
	"context"
	"github.com/google/uuid"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentPublicationFailureRecoversOriginalBytesAtomically(t *testing.T) {
	f, ledger := newLedgerAgentFixture(t, map[string]string{"a.txt": "base\n"})
	d := f.open(t, "a.txt")
	in := agentEdit(d, "agent\n")
	_, err := f.store.db.ExecContext(t.Context(), `CREATE TRIGGER interrupt_agent_receipt BEFORE UPDATE ON editor_agent_receipts BEGIN SELECT RAISE(ABORT, 'interrupted failure settlement'); END`)
	testutil.FailErr(t, "interrupt terminal settlement", err)
	f.service.SetOnChange(func(_ context.Context, change Change) {
		if change.ContentChanged {
			f.write(t, "a.txt", "outside\n")
		}
	})
	// The document accepted the edit; the interrupted publication is the result's outcome.
	result, err := f.service.ApplyAgentEdit(t.Context(), in)
	testutil.FailErr(t, "accept the edit", err)
	if result.Saved || result.PublicationError == nil {
		t.Fatalf("publication did not surface interrupted settlement: %+v", result)
	}
	mutation, err := f.store.Mutation(t.Context(), in.OperationID)
	testutil.FailErr(t, "read unsettled intent", err)
	if mutation.Status != "prepared" || string(mutation.AfterBytes) != "agent\n" {
		t.Fatalf("lost publication bytes: %+v", mutation)
	}
	_, err = f.store.db.ExecContext(t.Context(), `DROP TRIGGER interrupt_agent_receipt`)
	testutil.FailErr(t, "remove settlement failure", err)
	f.service.SetOnChange(nil)
	current, err := f.store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "read accepted agent head", err)
	_, err = f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: current.Revision}, Content: "agent with later typing\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "accept later typing", err)
	testutil.FailErr(t, "stop service", f.service.Close(t.Context()))
	restarted := New(f.store, ledger, ledger.History, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, restarted)
	replayed, err := restarted.ApplyAgentEdit(t.Context(), in)
	testutil.FailErr(t, "recover interrupted publication", err)
	if replayed.Saved || replayed.HeldVersionID == "" || replayed.Document.Draft != "agent with later typing\n" {
		t.Fatalf("recovery changed later work: %+v", replayed)
	}
	held, err := ledger.History.ReadRestorableVersion(t.Context(), f.project.ID, replayed.HeldVersionID)
	testutil.FailErr(t, "read original held publication", err)
	if text, ok := held.Text(); !ok || text != "agent\n" {
		t.Fatalf("held bytes = %q, readable=%v", text, ok)
	}
	mutation, err = f.store.Mutation(t.Context(), in.OperationID)
	testutil.FailErr(t, "read terminal receipt", err)
	if mutation.Status != "conflict" || mutation.Content != "" || len(mutation.AfterBytes) != 0 {
		t.Fatalf("terminal intent = %+v", mutation)
	}
}
