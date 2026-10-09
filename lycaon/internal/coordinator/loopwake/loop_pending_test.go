package loopwake

import (
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"testing"
)

func TestHasPendingLoopWakes(t *testing.T) {
	engine := NewLoopEngine()
	const id = "sess-pending"

	if engine.Nudges.HasPendingLoopWakes(id) {
		t.Fatal("expected no pending wakes on fresh engine")
	}

	engine.Nudges.enqueuePending(id, pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.Nudges.nudgeSeq.Add(1)})
	if !engine.Nudges.HasPendingLoopWakes(id) {
		t.Fatal("expected pending prompt-execution wake")
	}

	engine.Nudges.sessionPendingQueue(id).pop()
	if engine.Nudges.HasPendingLoopWakes(id) {
		t.Fatal("expected pending cleared after pop")
	}

	engine.Nudges.deferNudge(t.Context(), id, pendingLoopWake{wake: anchor.LegFinished})
	if !engine.Nudges.HasPendingLoopWakes(id) {
		t.Fatal("expected deferred worker-cycle wake")
	}
}
