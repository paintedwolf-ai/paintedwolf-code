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
	rawStage := toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerFailed, EffectCertainty: toolcontract.NoEffect}
	testutil.FailErr(t, "settle closure candidate", f.recorder.SettleChild(ctx, stage, toolcontract.ProgramChildSettlement{Result: rawStage, DetailReference: "output:stage"}))
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
	_, err = f.database.ExecContext(ctx, `UPDATE invocation_program_executions SET parent_execution_id=? WHERE id=?`, aggregate, stage)
	testutil.FailErr(t, "place fixture stage inside pipeline", err)
	testutil.FailErr(t, "settle pipeline with verified closure", f.recorder.SettleChild(ctx, aggregate, submission))
	var rawStatus int
	var closed bool
	row = f.database.QueryRowContext(ctx, `SELECT status_code,consumer_closed FROM invocation_program_executions WHERE id=?`, stage)
	testutil.FailErr(t, "read stage closure facts", row.Scan(&rawStatus, &closed))
	if rawStatus != 2 || !closed {
		t.Fatalf("stage facts changed: status=%d closed=%t", rawStatus, closed)
	}
}
