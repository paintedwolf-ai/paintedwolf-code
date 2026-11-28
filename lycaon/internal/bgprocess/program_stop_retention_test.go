package bgprocess

import (
	"context"
	"io"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramJobClearsOnStopAfterClaimAndOrdinaryExit(t *testing.T) {
	t.Run("explicit_stop", func(t *testing.T) {
		reg := NewRegistry(DefaultConfig(), Hooks{})
		t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })
		handle, err := reg.StartProgram(t.Context(), ProgramJobSpec{
			SessionID: "sess-1", ProjectID: "proj-1", CommandLine: "stop-clear",
			OnStop: func(TerminationReason) {},
			Run: func(ctx context.Context, _, _ io.Writer) (ProgramJobResult, error) {
				<-ctx.Done()
				return ProgramJobResult{ExitCode: 130}, ctx.Err()
			},
		})
		testutil.FailErr(t, "start program", err)
		_, err = reg.Lifecycle.Stop("sess-1", handle, TerminationStoppedByUser)
		testutil.FailErr(t, "stop program", err)
		_, _, err = reg.Lifecycle.Wait(t.Context(), "sess-1", handle)
		testutil.FailErr(t, "wait stopped program", err)

		reg.jobs.mu.Lock()
		proc := reg.jobs.sessions["sess-1"][handle]
		onStop := proc.onStop
		reg.jobs.mu.Unlock()
		if onStop != nil {
			t.Fatal("proc.onStop remained non-nil after explicit stop")
		}
	})

	t.Run("ordinary_exit", func(t *testing.T) {
		reg := NewRegistry(DefaultConfig(), Hooks{})
		t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })
		handle, err := reg.StartProgram(t.Context(), ProgramJobSpec{
			SessionID: "sess-1", ProjectID: "proj-1", CommandLine: "exit-clear",
			OnStop: func(TerminationReason) {},
			Run: func(_ context.Context, _, _ io.Writer) (ProgramJobResult, error) {
				return ProgramJobResult{ExitCode: 0}, nil
			},
		})
		testutil.FailErr(t, "start program", err)
		_, _, err = reg.Lifecycle.Wait(t.Context(), "sess-1", handle)
		testutil.FailErr(t, "wait exited program", err)

		reg.jobs.mu.Lock()
		proc := reg.jobs.sessions["sess-1"][handle]
		onStop := proc.onStop
		reg.jobs.mu.Unlock()
		if onStop != nil {
			t.Fatal("proc.onStop remained non-nil after ordinary settlement")
		}
	})
}
