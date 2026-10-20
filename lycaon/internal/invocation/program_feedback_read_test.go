package invocation_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
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
			_, err := f.database.ExecContext(t.Context(), `UPDATE invocation_program_executions SET canonical=? WHERE id=?`, strings.Repeat("later ", 1<<15), execution)
			testutil.FailErr(t, "commit concurrent metadata growth", err)
		}
		held += n
		return func() { held -= n }, nil
	})
	lease, err := f.recorder.ReadProgramFeedback(t.Context(), f.session, f.program, execution, 2, admission)
	testutil.FailErr(t, "admit coherent feedback snapshot", err)
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
