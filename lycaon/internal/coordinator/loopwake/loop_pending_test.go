package loopwake

import (
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"testing"
)

func TestHasPendingLoopWakes(t *testing.T) {
	engine := &LoopEngine{}
	const id = "sess-pending"

	if engine.HasPendingLoopWakes(id) {
		t.Fatal("expected no pending wakes on fresh engine")
	}

	engine.enqueuePending(id, pendingLoopWake{wake: anchor.WorkerTaskFinished, seq: engine.nudgeSeq.Add(1)})
	if !engine.HasPendingLoopWakes(id) {
		t.Fatal("expected pending prompt-execution wake")
	}

	engine.sessionPendingQueue(id).pop()
	if engine.HasPendingLoopWakes(id) {
		t.Fatal("expected pending cleared after pop")
	}

	engine.deferNudge(t.Context(), id, pendingLoopWake{wake: anchor.LegFinished})
	if !engine.HasPendingLoopWakes(id) {
		t.Fatal("expected deferred worker-cycle wake")
	}
}
