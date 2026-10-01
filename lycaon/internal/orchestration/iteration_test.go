package orchestration

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIterationCapExceeded(t *testing.T) {
	cap := NewInMemoryIterationCap(8)
	ctx := context.Background()
	agentID, taskID := "implementer", "task-1"

	for i := 0; i < 8; i++ {
		if _, err := cap.Track(ctx, agentID, taskID); err != nil {
			testutil.FailErr(t, "cap.Track failed", err)
		}
		dec, err := cap.Check(ctx, agentID, taskID)
		testutil.FailErr(t, "cap.Check failed", err)
		if dec != nil && dec.ShouldTerminate {
			t.Fatalf("iteration %d should not terminate yet", i+1)
		}
	}
	if _, err := cap.Track(ctx, agentID, taskID); err != nil {
		testutil.FailErr(t, "cap.Track failed", err)
	}
	dec, err := cap.Check(ctx, agentID, taskID)
	testutil.FailErr(t, "cap.Check failed", err)
	if dec == nil || !dec.ShouldTerminate {
		t.Fatal("expected termination on 9th iteration")
	}
	if dec.Reason != TerminationReasonMaxIterations {
		t.Fatalf("reason = %q", dec.Reason)
	}
	if dec.NextAction != NextActionAbort {
		t.Fatalf("next_action = %q", dec.NextAction)
	}
}

func TestIterationTrackerReset(t *testing.T) {
	cap := NewInMemoryIterationCap(8)
	ctx := context.Background()
	agentID := "implementer"

	for i := 0; i < 5; i++ {
		if _, err := cap.Track(ctx, agentID, "task-a"); err != nil {
			testutil.FailErr(t, "cap.Track failed", err)
		}
	}
	st, err := cap.Track(ctx, agentID, "task-b")
	testutil.FailErr(t, "cap.Track failed", err)
	if st.Count != 1 {
		t.Fatalf("new task count = %d want 1", st.Count)
	}
}

func TestStuckDetectionThreeIdenticalErrors(t *testing.T) {
	cap := NewInMemoryIterationCap(8)
	ctx := context.Background()
	agentID, taskID := "implementer", "task-1"
	errSame := errors.New("same failure")

	for i := 0; i < MaxStuckIterations; i++ {
		st, err := cap.NoteError(ctx, agentID, taskID, errSame)
		testutil.FailErr(t, "cap.NoteError failed", err)
		if i < MaxStuckIterations-1 && st.Stuck {
			t.Fatalf("stuck too early at error %d", i+1)
		}
	}
	dec, err := cap.Check(ctx, agentID, taskID)
	testutil.FailErr(t, "cap.Check failed", err)
	if dec == nil || !dec.ShouldTerminate {
		t.Fatal("expected stuck termination")
	}
	if dec.Reason != TerminationReasonStuck {
		t.Fatalf("reason = %q", dec.Reason)
	}
	if dec.NextAction != NextActionReassign {
		t.Fatalf("next_action = %q", dec.NextAction)
	}
}

func TestSelfTerminationConvergence(t *testing.T) {
	st := NewDefaultSelfTermination()
	dec, err := st.Evaluate(context.Background(), TerminationContext{
		AgentID:    "implementer",
		TaskID:     "task-1",
		Confidence: 0.9,
	})
	testutil.FailErr(t, "st.Evaluate failed", err)
	if dec == nil || !dec.ShouldTerminate {
		t.Fatal("expected convergence termination")
	}
	if dec.Reason != TerminationReasonConvergence {
		t.Fatalf("reason = %q", dec.Reason)
	}
}

func TestSelfTerminationOutputStable(t *testing.T) {
	st := NewDefaultSelfTermination()
	dec, err := st.Evaluate(context.Background(), TerminationContext{
		AgentID:      "implementer",
		TaskID:       "task-1",
		OutputStable: true,
	})
	testutil.FailErr(t, "st.Evaluate failed", err)
	if dec == nil || !dec.ShouldTerminate {
		t.Fatal("expected output-stable termination")
	}
	if dec.Reason != TerminationReasonConvergence {
		t.Fatalf("reason = %q", dec.Reason)
	}
}

func TestTopologySpecOverridesDefaultCap(t *testing.T) {
	cap := NewInMemoryIterationCap(8)
	ctx := context.Background()
	agentID, taskID := "implementer", "task-1"
	spec := TopologySpec{IterationCap: 3}

	for i := 0; i < 3; i++ {
		if _, err := cap.Track(ctx, agentID, taskID); err != nil {
			testutil.FailErr(t, "cap.Track failed", err)
		}
		dec, err := cap.CheckMax(ctx, agentID, taskID, EffectiveIterationCap(spec))
		testutil.FailErr(t, "cap.CheckMax failed", err)
		if dec != nil && dec.ShouldTerminate {
			t.Fatalf("iteration %d should not terminate yet", i+1)
		}
	}
	if _, err := cap.Track(ctx, agentID, taskID); err != nil {
		testutil.FailErr(t, "cap.Track failed", err)
	}
	dec, err := cap.CheckMax(ctx, agentID, taskID, EffectiveIterationCap(spec))
	testutil.FailErr(t, "cap.CheckMax failed", err)
	if dec == nil || !dec.ShouldTerminate {
		t.Fatal("expected termination on 4th iteration with cap=3")
	}
	if dec.Reason != TerminationReasonMaxIterations {
		t.Fatalf("reason = %q", dec.Reason)
	}
}
