package history

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSynthesisRunnerBoundsStalledWorkAndCancelsWithoutJoining(t *testing.T) {
	runner := NewSynthesisRunner()
	release := make(chan struct{})
	started := make(chan context.Context, synthesisConcurrency)
	for i := 0; i < synthesisConcurrency; i++ {
		if !runner.Start(t.Context(), fmt.Sprint(i), func(ctx context.Context) { started <- ctx; <-release }) {
			t.Fatal("available slot declined")
		}
	}
	first := <-started
	if runner.Start(t.Context(), "excess", func(context.Context) { t.Error("excess work ran") }) {
		t.Fatal("unbounded synthesis admission")
	}
	if runner.Start(t.Context(), "0", func(context.Context) { t.Error("duplicate work ran") }) {
		t.Fatal("duplicate synthesis admission")
	}
	runner.Cancel("0")
	runner.Stop()
	select {
	case <-first.Done():
	case <-t.Context().Done():
		t.Fatal("synthesis cancellation did not reach the provider")
	}
	if runner.Start(t.Context(), "after stop", func(context.Context) { t.Error("sealed work ran") }) {
		t.Fatal("admitted work after stop")
	}
	close(release)
	testutil.FailErr(t, "drain synthesis", runner.WaitContext(t.Context()))
}

func TestSynthesisRunnerReleasesCompletedSlots(t *testing.T) {
	runner := NewSynthesisRunner()
	if runner.Start(t.Context(), "", func(context.Context) {}) || runner.Start(t.Context(), "nil", nil) {
		t.Fatal("invalid synthesis admitted")
	}
	if !runner.Start(t.Context(), "one", func(context.Context) {}) {
		t.Fatal("first job declined")
	}
	testutil.FailErr(t, "first synthesis", runner.WaitContext(t.Context()))
	if !runner.Start(t.Context(), "one", func(context.Context) {}) {
		t.Fatal("completed slot was retained")
	}
	testutil.FailErr(t, "second synthesis", runner.WaitContext(t.Context()))
	runner.Stop()
	if runner.Start(t.Context(), "sealed", func(context.Context) {}) {
		t.Fatal("sealed runner admitted work")
	}
}

func TestSynthesisRunnerRetainsOnlyTheLatestSuccessor(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			runner := NewSynthesisRunner()
			release := make(chan struct{})
			if !runner.Start(t.Context(), "session", func(context.Context) { <-release }) {
				t.Fatal("first synthesis declined")
			}
			runs := 0
			runner.StartLatest(t.Context(), "session", func(context.Context) { t.Error("superseded job ran") })
			runner.StartLatest(t.Context(), "session", func(context.Context) { runs++ })
			if cancel {
				runner.Cancel("session")
			}
			close(release)
			testutil.FailErr(t, "drain synthesis and any successor", runner.WaitContext(t.Context()))
			want := 1
			if cancel {
				want = 0
			}
			if runs != want {
				t.Fatalf("latest job ran %d times, want %d", runs, want)
			}
		})
	}
}
