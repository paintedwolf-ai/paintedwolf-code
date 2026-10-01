package coordinator_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
)

func TestRuntimePromptLoopRefreshesDeps(t *testing.T) {
	calls := 0
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{
		LoopDeps: func() promptloop.PromptLoopDeps {
			calls++
			return promptloop.PromptLoopDeps{}
		},
	})
	if rt.PromptLoop() == nil {
		t.Fatal("expected loop")
	}
	if calls != 1 {
		t.Fatalf("loop deps calls = %d want 1", calls)
	}
}
