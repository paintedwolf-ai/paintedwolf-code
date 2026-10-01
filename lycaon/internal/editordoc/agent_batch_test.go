package editordoc

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentBatchRejectsInvalidSecondAnchorBeforeAnyAcceptance(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	a, b := f.open(t, "a.txt"), f.open(t, "b.txt")
	_, err := f.service.Pin(t.Context(), f.project.ID, b.ID, b.Revision)
	testutil.FailErr(t, "pin second read", err)
	_, err = f.service.ReplaceSnapshot(t.Context(), b.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: b.Revision}, Content: "peer", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "move second anchor", err)
	*f.changes = nil
	_, err = f.service.ApplyAgentEdits(t.Context(), []AgentEdit{agentEdit(a, "a agent"), agentEdit(b, "replacement")})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("batch error = %v", err)
	}
	stored, err := f.store.Get(t.Context(), a.ID)
	testutil.FailErr(t, "read unchanged first head", err)
	if stored.Revision != a.Revision || stored.Draft != "a" || f.disk(t, "a.txt") != "a" || len(*f.changes) != 0 {
		t.Fatalf("invalid batch changed first head: %+v, events=%d", stored, len(*f.changes))
	}
}

func TestAgentBatchRollsBackAllHeadsAndReloadsSpeculation(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	a, b := f.open(t, "a.txt"), f.open(t, "b.txt")
	_, err := f.store.db.ExecContext(t.Context(), `CREATE TRIGGER reject_second_editor_head BEFORE UPDATE ON editor_documents WHEN NEW.path='b.txt' BEGIN SELECT RAISE(ABORT, 'injected second-head failure'); END`)
	testutil.FailErr(t, "inject second head failure", err)
	inputs := []AgentEdit{agentEdit(a, "a agent"), agentEdit(b, "b agent")}
	*f.changes = nil
	_, err = f.service.ApplyAgentEdits(t.Context(), inputs)
	if err == nil {
		t.Fatal("batch ignored second-head failure")
	}
	for _, before := range []*Document{a, b} {
		stored, err := f.store.Get(t.Context(), before.ID)
		testutil.FailErr(t, "read rolled back head", err)
		if stored.Revision != before.Revision || stored.Draft != before.Draft || f.disk(t, before.Path) != before.Draft {
			t.Fatalf("partial batch acceptance: %+v", stored)
		}
	}
	var receipts int
	testutil.FailErr(t, "count rolled back receipts", f.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM editor_agent_receipts`).Scan(&receipts))
	if receipts != 0 || len(*f.changes) != 0 {
		t.Fatalf("uncommitted batch exposed receipts=%d events=%d", receipts, len(*f.changes))
	}
	_, err = f.store.db.ExecContext(t.Context(), `DROP TRIGGER reject_second_editor_head`)
	testutil.FailErr(t, "remove injected failure", err)
	results, err := f.service.ApplyAgentEdits(t.Context(), inputs)
	testutil.FailErr(t, "retry complete batch", err)
	for i, result := range results {
		if !result.Saved || result.Document.Draft != inputs[i].Content {
			t.Fatalf("retry = %+v", result)
		}
	}
}

func TestAgentBatchPublishesOnlyAfterBothHeadsCommitAndReplaysAfterRestart(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	a, b := f.open(t, "a.txt"), f.open(t, "b.txt")
	inputs := []AgentEdit{agentEdit(a, "a agent"), agentEdit(b, "b agent")}
	seen := 0
	f.service.SetOnChange(func(ctx context.Context, change Change) {
		if !change.ContentChanged {
			return
		}
		seen++
		for _, in := range inputs {
			stored, err := f.store.Get(ctx, in.DocumentID)
			testutil.FailErr(t, "observe atomic heads", err)
			if stored.Draft != in.Content {
				t.Fatalf("event exposed partial batch: %+v", stored)
			}
		}
	})
	results, err := f.service.ApplyAgentEdits(t.Context(), inputs)
	testutil.FailErr(t, "accept batch", err)
	if seen != 2 {
		t.Fatalf("accepted events = %d", seen)
	}
	testutil.FailErr(t, "close first service", f.service.Close(t.Context()))
	restarted := New(f.store, f.recorder, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, restarted)
	replayed, err := restarted.ApplyAgentEdits(t.Context(), inputs)
	testutil.FailErr(t, "replay batch", err)
	for i, result := range replayed {
		if !result.Saved || result.Document.Revision != results[i].Document.Revision || result.Document.Draft != inputs[i].Content {
			t.Fatalf("replayed batch = %+v", result)
		}
	}
}
