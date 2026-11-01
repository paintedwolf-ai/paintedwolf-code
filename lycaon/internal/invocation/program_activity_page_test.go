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
		if page.Activities[0].Outcome != "Finished" {
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

func TestActivitySummaryCountsRepetitionsBeyondReceiptPage(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	id := child.Invocation().Identity.ExecutionID
	_, err := f.database.ExecContext(t.Context(), `UPDATE invocation_program_executions SET state='completed',status_code=0,effect_certainty='none' WHERE id=?`, id)
	testutil.FailErr(t, "settle representative", err)
	// Repeated executions share one immutable parser node.
	_, err = f.database.ExecContext(t.Context(), `WITH RECURSIVE repetitions(n) AS (SELECT 2 UNION ALL SELECT n+1 FROM repetitions WHERE n<10705)
 INSERT INTO invocation_program_executions (id,program_id,node_id,ordinal,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,canonical,state,owner,lifecycle,native_command,effect_mode,effect_classes_json,started_at,status_code,effect_certainty)
 SELECT 'repeat-'||n,program_id,node_id,n,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,canonical,'completed',owner,lifecycle,native_command,effect_mode,effect_classes_json,started_at,CASE WHEN n=10705 THEN 1 ELSE 0 END,'none'
 FROM invocation_program_executions,repetitions WHERE id=?`, id)
	testutil.FailErr(t, "record repeated executions", err)
	summary, err := f.recorder.ActivitySummary(t.Context(), f.session, f.program)
	testutil.FailErr(t, "read full activity summary", err)
	if len(summary.Activities) != 1 || summary.Activities[0].ExecutionCount != 10705 || summary.Activities[0].FailedCount != 1 || len(summary.Activities[0].ExecutionIDs) != 1 {
		t.Fatalf("summary lost later repetitions: %+v", summary.Activities)
	}
	first, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, id, "", 6)
	testutil.FailErr(t, "read activity page", err)
	if len(first.Executions) != 6 || first.ExecutionsOmitted != 10699 {
		t.Fatalf("activity paging: %+v", first)
	}
	next, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, id, first.NextCursor, 6)
	testutil.FailErr(t, "read next activity page", err)
	if len(next.Executions) != 6 || next.Executions[0].ID == first.Executions[0].ID || len(next.Activities) != 1 || next.Activities[0].ID != id {
		t.Fatal("receipt paging changed activity identity")
	}
	if _, err := f.recorder.ReadProgramActivityPage(t.Context(), f.session, f.program, "another-activity", first.NextCursor, 6); err == nil {
		t.Fatal("cursor crossed activity boundary")
	}
}
