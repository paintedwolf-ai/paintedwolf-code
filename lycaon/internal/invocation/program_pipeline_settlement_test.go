package invocation_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestPipelineClosureSettlementRequiresItsOwnSettledChild(t *testing.T) {
	f := newProgramReceiptFixture(t)
	aggregate := f.child(t).Invocation().Identity.ExecutionID
	stage := f.child(t).Invocation().Identity.ExecutionID
	completed := toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}
	submission := toolcontract.ProgramChildSettlement{Result: completed, DetailReference: "output:pipeline", ConsumerClosedExecutions: []string{stage}}
	ctx := t.Context()
	testutil.FailErr(t, "settle closure candidate", f.recorder.SettleChild(ctx, stage, toolcontract.ProgramChildSettlement{Result: completed, DetailReference: "output:stage"}))
	if err := f.recorder.SettleChild(ctx, aggregate, submission); err == nil {
		t.Fatal("ordinary invocation recorded a pipeline fact")
	}
	_, err := f.database.ExecContext(ctx, `UPDATE invocation_program_executions SET operation_id='program |' WHERE id=?`, aggregate)
	testutil.FailErr(t, "mark fixture aggregate as a pipeline", err)
	if err := f.recorder.SettleChild(ctx, aggregate, submission); err == nil {
		t.Fatal("pipeline annotated a sibling")
	}
	row := f.database.QueryRowContext(ctx, `SELECT state FROM invocation_program_executions WHERE id=?`, aggregate)
	var state string
	testutil.FailErr(t, "read aggregate after invalid settlement", row.Scan(&state))
	if state != "prepared" {
		t.Fatalf("aggregate partially settled with invalid closure fact: %s", state)
	}
}
