package invocation_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestProgramActivitiesKeepLedgerIdentityAndSourceCoordinates(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	page, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
	testutil.FailErr(t, "read activity receipt page", err)
	if len(page.Activities) != 1 || page.Activities[0].ID != child.Invocation().Identity.ExecutionID || page.Activities[0].Spans[0].SourceRef != f.sourceStart.Version.SourceReference {
		t.Fatalf("activity lost ledger identity: %+v", page.Activities)
	}
	if page.Program.ActivityRevision == 0 {
		t.Fatal("missing monotonic trace revision")
	}
	if _, err := f.recorder.ActivitySummary(t.Context(), "other-session", f.program); err == nil {
		t.Fatal("activity summary crossed session boundary")
	}
}

func TestProgramActivityObserverSeesCommittedSettlement(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	observed := 0
	ctx := invocation.WithActivityObserver(t.Context(), func(ctx context.Context, program, _ string) {
		page, err := f.recorder.ReadProgramPage(ctx, f.session, program, "", 10)
		testutil.FailErr(t, "read committed activity update", err)
		if page.Activities[0].Outcome != "Exited 0" {
			t.Fatalf("observed uncommitted outcome: %+v", page.Activities)
		}
		observed++
	})
	result := toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}
	testutil.FailErr(t, "settle observed child", f.recorder.SettleChild(ctx, child.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: result, OwnerInvoked: true, DetailReference: "output:done"}))
	if observed != 1 {
		t.Fatalf("updates = %d", observed)
	}
}
