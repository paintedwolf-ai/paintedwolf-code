package invocation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type feedbackAdmissionFunc func(context.Context, int64) (func(), error)

func (f feedbackAdmissionFunc) ReserveProgramState(ctx context.Context, n int64) (func(), error) {
	return f(ctx, n)
}

func TestProgramFeedbackAdmitsAndHydratesOneSnapshot(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	execution := child.Invocation().Identity.ExecutionID
	held, calls := int64(0), 0
	admission := feedbackAdmissionFunc(func(_ context.Context, n int64) (func(), error) {
		calls++
		if calls == 2 {
			// The size query has established a reader snapshot. A real concurrent
			// writer may grow a selected field, but it cannot alter this hydration.
			_, err := f.database.ExecContext(t.Context(), `UPDATE invocation_program_executions SET canonical=?,state='refused',host_stop_code='LATER_REFUSAL',owner_invoked=1 WHERE id=?`, strings.Repeat("later ", 1<<15), execution)
			testutil.FailErr(t, "commit concurrent metadata growth", err)
		}
		held += n
		return func() { held -= n }, nil
	})
	lease, err := f.recorder.ReadProgramFeedback(t.Context(), f.session, f.program, execution, 2, admission)
	testutil.FailErr(t, "admit coherent feedback snapshot", err)
	if lease.Execution.ID != execution || lease.Execution.State != "running" || lease.Execution.OwnerInvoked || lease.Execution.HostStopCode != "" {
		t.Fatalf("exact execution crossed reader snapshot: %+v", lease.Execution)
	}
	if calls != 2 || held != lease.RequiredStateBytes || lease.RequiredStateBytes < 64<<10 || lease.Binding.Invocation.Identity.ExecutionID != execution || len(lease.Projection.Unknown) != 1 || lease.Projection.Unknown[0].Receipt.Command != "cat input" {
		t.Fatalf("feedback crossed snapshot or accounting: %+v held=%d calls=%d", lease, held, calls)
	}
	lease.Close()
	lease.Close()
	if held != 0 {
		t.Fatalf("feedback lease leaked %d", held)
	}
	var current string
	testutil.FailErr(t, "confirm concurrent update actually landed", f.database.QueryRowContext(t.Context(), `SELECT canonical FROM invocation_program_executions WHERE id=?`, execution).Scan(&current))
	if len(current) < 1<<15 {
		t.Fatal("growth fixture did not change current row")
	}
}

func TestProgramFeedbackStateRefusalPrecedesHydration(t *testing.T) {
	f := newProgramReceiptFixture(t)
	child := f.child(t)
	f.start(t, child)
	denied := errors.New("fixture retained state limit")
	calls, held := 0, int64(0)
	admission := feedbackAdmissionFunc(func(_ context.Context, n int64) (func(), error) {
		calls++
		if calls == 2 {
			return nil, denied
		}
		held += n
		return func() { held -= n }, nil
	})
	lease, err := f.recorder.ReadProgramFeedback(t.Context(), f.session, f.program, child.Invocation().Identity.ExecutionID, 2, admission)
	if lease != nil || !errors.Is(err, denied) || held != 0 {
		t.Fatalf("state refusal lost or base lease leaked: %v held=%d", err, held)
	}
}

func TestProgramFeedbackLeaseZeroAndNilClose(t *testing.T) {
	var absent *invocation.ProgramFeedbackLease
	absent.Close()
	var zero invocation.ProgramFeedbackLease
	zero.Close()
	zero.Close()
}

func TestProgramFeedbackTargetsRequestedIterationNotLastStoppedChild(t *testing.T) {
	f := newProgramReceiptFixture(t)
	earlier := f.child(t)
	later := f.child(t)
	f.start(t, earlier)
	f.start(t, later)
	for i, bound := range []toolcontract.BoundOperation{earlier, later} {
		code := []string{"FIRST_OWNER_REFUSAL", "SECOND_OWNER_REFUSAL"}[i]
		result := toolcontract.ProducerResult{Terminal: toolcontract.ProducerRefused, StopCode: code, EffectCertainty: toolcontract.NoEffect, Evidence: toolcontract.OperationEvidence{Owner: "filesystem", Kind: "refusal"}}
		testutil.FailErr(t, "settle actual independent iteration", f.recorder.SettleChild(t.Context(), bound.Invocation().Identity.ExecutionID, toolcontract.ProgramChildSettlement{Result: result, OwnerInvoked: true, DetailReference: "output:owner-refusal-" + code}))
	}
	admission := feedbackAdmissionFunc(func(context.Context, int64) (func(), error) { return func() {}, nil })
	lease, err := f.recorder.ReadProgramFeedback(t.Context(), f.session, f.program, earlier.Invocation().Identity.ExecutionID, 1, admission)
	testutil.FailErr(t, "read exact earlier stopped iteration", err)
	defer lease.Close()
	if lease.Execution.ID != earlier.Invocation().Identity.ExecutionID || lease.Execution.StopCode != "FIRST_OWNER_REFUSAL" || !lease.Execution.OwnerInvoked {
		t.Fatalf("feedback substituted later loop child: %+v", lease.Execution)
	}
}

func TestProgramFeedbackBoundsNonzeroExecutions(t *testing.T) {
	f := newProgramReceiptFixture(t)
	var executions []string
	for _, status := range []int{1, 0, 7, 2} {
		child := f.child(t)
		f.start(t, child)
		id := child.Invocation().Identity.ExecutionID
		executions = append(executions, id)
		testutil.FailErr(t, "settle child", f.recorder.SettleChild(t.Context(), id, toolcontract.ProgramChildSettlement{
			Result: toolcontract.ProducerResult{Status: status, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, OwnerInvoked: true,
		}))
	}
	held := int64(0)
	admission := feedbackAdmissionFunc(func(_ context.Context, n int64) (func(), error) {
		held += n
		return func() { held -= n }, nil
	})
	lease, err := f.recorder.ReadProgramOutcomeFeedback(t.Context(), f.session, f.program, 2, admission)
	testutil.FailErr(t, "project bounded nonzero children", err)
	p := lease.Projection
	if p.NonzeroExecutions != 3 || p.OmittedNonzero != 1 || len(p.Nonzero) != 2 {
		t.Fatalf("nonzero accounting = %+v", p)
	}
	for i, child := range p.Nonzero {
		if child.Receipt.ID != executions[3-i] || child.Receipt.StatusCode == nil || *child.Receipt.StatusCode == 0 || !child.Receipt.OwnerInvoked || child.SourceUnitID != "submitted" || child.Span != f.span {
			t.Fatalf("lost child identity or status: %+v", child)
		}
	}
	if held != lease.RequiredStateBytes {
		t.Fatalf("state admission = %d want %d", held, lease.RequiredStateBytes)
	}
	lease.Close()
	if held != 0 {
		t.Fatalf("leaked state admission: %d", held)
	}
	if _, err := f.recorder.ProjectProgram(t.Context(), "other-session", f.program, 2); err == nil {
		t.Fatal("projection crossed session boundary")
	}
}
