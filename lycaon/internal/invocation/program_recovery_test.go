package invocation_test

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestProgramBootRecoverySettlesParentAndChildrenAtomically(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	// A parent ledger fault must roll back child interruption too. No worker is
	// restarted and no owner callback is reachable through this recovery operation.
	_, err := f.database.ExecContext(t.Context(), `CREATE TRIGGER fixture_parent_fault BEFORE UPDATE ON invocation_receipts WHEN NEW.status='interrupted' BEGIN SELECT RAISE(ABORT,'fixture ledger fault'); END`)
	testutil.FailErr(t, "install actual transaction fault", err)
	if _, err = f.recorder.InterruptRunning(t.Context()); err == nil {
		t.Fatal("parent ledger fault accepted")
	}
	page, err := f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
	testutil.FailErr(t, "inspect rolled back interruption", err)
	if page.Program.State != "running" || page.Executions[0].State != "running" {
		t.Fatalf("partial recovery commit: %+v", page)
	}
	_, err = f.database.ExecContext(t.Context(), `DROP TRIGGER fixture_parent_fault`)
	testutil.FailErr(t, "release fixture fault", err)
	count, err := f.recorder.InterruptRunningSession(t.Context(), f.session)
	testutil.FailErr(t, "interrupt actual parent and program", err)
	if count != 1 {
		t.Fatalf("parent interruption count=%d", count)
	}
	page, err = f.recorder.ReadProgramPage(t.Context(), f.session, f.program, "", 10)
	testutil.FailErr(t, "inspect durable interrupted children", err)
	if page.Program.State != "interrupted" || page.Executions[0].State != "interrupted" || page.Executions[0].EffectCertainty != "unknown" || page.Executions[0].StatusCode != nil || page.Nodes[1].State != "not_started" {
		t.Fatalf("incorrect recovery facts: %+v", page)
	}
	receipts, err := f.recorder.ListSession(t.Context(), f.session)
	testutil.FailErr(t, "inspect actual model receipt", err)
	if len(receipts) != 1 || receipts[0].Status != api.InvocationStatusInterrupted || receipts[0].Failure == nil || receipts[0].Failure.Retryable {
		t.Fatalf("program marked safe to replay: %+v", receipts)
	}
	count, err = f.recorder.InterruptRunning(t.Context())
	testutil.FailErr(t, "repeat boot reconciliation", err)
	if count != 0 {
		t.Fatalf("repeated interruption=%d", count)
	}
}
