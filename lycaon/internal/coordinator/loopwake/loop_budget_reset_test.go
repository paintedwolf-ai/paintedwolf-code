package loopwake

import (
	"testing"
)

func TestLoopResetBudget(t *testing.T) {
	engine := NewLoopEngine()
	key := loopBudgetKey{sessionID: "session-1", runID: "run-1"}
	engine.Admission.budget.Store(key, 3)
	engine.Admission.ResetBudget("run-1")
	if _, ok := engine.Admission.budget.Load(key); ok {
		t.Fatal("budget should be cleared")
	}
}
