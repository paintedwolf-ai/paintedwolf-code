package invocation_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestPreparedChildPublishesOnlyAfterAdmissionOrApprovalWait(t *testing.T) {
	f := newProgramReceiptFixture(t)
	ctx := t.Context()
	prepared := f.child(t)
	for _, stage := range []string{"prepared", "refused"} {
		if stage == "refused" {
			result := toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerRefused, StopCode: "COMMAND_OPERAND_INVALID", EffectCertainty: toolcontract.NoEffect}
			testutil.FailErr(t, "settle refusal", f.recorder.SettleChild(ctx, prepared.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: result}))
		}
		summary, err := f.recorder.LiveActivitySummary(ctx, f.session, f.program)
		testutil.FailErr(t, "read live activity", err)
		if len(summary.Activities) != 0 {
			t.Fatalf("%s child populated live activity: %+v", stage, summary.Activities)
		}
	}
	child := f.child(t)
	seen := 0
	waitCtx := invocation.WithActivityObserver(ctx, invocation.ActivityObserver{Execution: func(ctx context.Context, program, execution string) {
		seen++
		summary, err := f.recorder.LiveActivitySummary(ctx, f.session, program)
		testutil.FailErr(t, "read committed approval wait", err)
		for _, row := range summary.Activities {
			if row.WaitingCount != 0 {
				return
			}
		}
		t.Fatalf("approval wait for %s missing from %+v", execution, summary.Activities)
	}})
	marker := invocation.ProgramAdmissionRecorder{Recorder: f.recorder}
	marker.BeforeNativeApprovalWait(waitCtx, child, "checkpoint-1")
	if seen != 1 {
		t.Fatalf("committed wait notifications = %d", seen)
	}
}
