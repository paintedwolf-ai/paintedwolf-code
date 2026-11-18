package orchestration

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestOrchestratorStatusRaceWithCancel(t *testing.T) {
	orch := NewOrchestratorImpl(OrchestratorDeps{})
	const runID = "race-run"
	orch.registerRun(runID, &runState{runID: runID, phase: "pipeline", active: true})

	// The work is counted, so the detector sees the same interleavings on any host.
	const rounds = 2000
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range rounds {
			_ = orch.Cancel(ctx, runID, TerminationReasonHumanAbort)
		}
	}()
	go func() {
		defer wg.Done()
		for range rounds {
			if _, err := orch.Status(ctx, runID); err != nil {
				t.Errorf("Status: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

// TestOrchestratorRegisterRunPrunesOldCompletedRuns guards the retention
// policy for OrchestratorImpl.runs: entries must stay queryable for a bounded
// window after completion (callers poll Status once immediately after the
// synchronous Run call returns), but must not accumulate forever.
func TestOrchestratorRegisterRunPrunesOldCompletedRuns(t *testing.T) {
	orch := NewOrchestratorImpl(OrchestratorDeps{})

	orch.registerRun("old-done", &runState{
		runID: "old-done", active: false,
		completedAt: time.Now().Add(-runRetentionWindow - time.Minute),
	})
	orch.registerRun("recent-done", &runState{
		runID: "recent-done", active: false,
		completedAt: time.Now().Add(-time.Second),
	})
	orch.registerRun("still-active", &runState{runID: "still-active", active: true})

	// Registering a new run opportunistically sweeps runs completed more than
	// runRetentionWindow ago.
	orch.registerRun("new-run", &runState{runID: "new-run", active: true})

	orch.mu.RLock()
	_, oldPresent := orch.runs["old-done"]
	_, recentPresent := orch.runs["recent-done"]
	_, activePresent := orch.runs["still-active"]
	_, newPresent := orch.runs["new-run"]
	orch.mu.RUnlock()

	if oldPresent {
		t.Error("run completed well past the retention window should have been pruned")
	}
	if !recentPresent {
		t.Error("recently completed run should still be queryable via Status")
	}
	if !activePresent {
		t.Error("an active run must never be pruned")
	}
	if !newPresent {
		t.Error("newly registered run should be present")
	}
}
