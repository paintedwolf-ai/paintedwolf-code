//go:build integration

package orchestration

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOrchestratorHookAbortsOnCap(t *testing.T) {
	o := NewOrchestratorImpl(OrchestratorDeps{})
	spec := TopologySpec{IterationCap: MaxIterations}
	agentID, taskID := "implementer", "task-1"
	ctx := context.Background()

	var hookErr error
	for i := 0; i < 9; i++ {
		if _, err := o.iterationCap.Track(ctx, agentID, taskID); err != nil {
			testutil.FailErr(t, "o.iterationCap.Track failed", err)
		}
		hookErr = o.checkIteration(ctx, spec, agentID, taskID, TerminationContext{
			AgentID: agentID,
			TaskID:  taskID,
		})
		if hookErr != nil {
			if i != 8 {
				t.Fatalf("hook failed early on iteration %d: %v", i+1, hookErr)
			}
			break
		}
	}
	if hookErr == nil {
		t.Fatal("expected cap abort on 9th iteration")
	}
	if !strings.Contains(hookErr.Error(), string(TerminationReasonMaxIterations)) {
		t.Fatalf("unexpected error: %v", hookErr)
	}
}

func TestOrchestratorHookConvergenceExit(t *testing.T) {
	o := NewOrchestratorImpl(OrchestratorDeps{})
	spec := TopologySpec{IterationCap: MaxIterations}
	ctx := context.Background()

	if _, err := o.iterationCap.Track(ctx, "implementer", "task-1"); err != nil {
		testutil.FailErr(t, "o.iterationCap.Track failed", err)
	}
	err := o.checkIteration(ctx, spec, "implementer", "task-1", TerminationContext{
		AgentID:    "implementer",
		TaskID:     "task-1",
		Confidence: 0.95,
	})
	if err == nil {
		t.Fatal("expected self-termination")
	}
	if !strings.Contains(err.Error(), string(TerminationReasonConvergence)) {
		t.Fatalf("unexpected error: %v", err)
	}
}
