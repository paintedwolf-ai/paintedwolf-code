package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
)

func TestCoordinatorLLMLoopProgressGuardedOnAllCoordinatorTurns(t *testing.T) {
	t.Parallel()

	userTurn := coordinatorLLMLoopProgress("coordinator", surface.SurfaceImplementDispatch, "Starting workers", false, 0, 8, false)
	if userTurn == nil || !userTurn.Guarded {
		t.Fatalf("user turn loop = %+v want guarded", userTurn)
	}
	if userTurn.HostTurn {
		t.Fatal("user turn must not set host_turn")
	}
	if userTurn.Iteration != 1 || userTurn.MaxIterations != 8 {
		t.Fatalf("iteration = %d/%d want 1/8 on every coordinator turn", userTurn.Iteration, userTurn.MaxIterations)
	}
	if userTurn.ProseFinish {
		t.Fatal("ordinary user turn must not set prose_finish")
	}

	hostTurn := coordinatorLLMLoopProgress("coordinator", surface.SurfaceImplementDispatch, "Starting workers", true, 2, 8, false)
	if hostTurn == nil || !hostTurn.Guarded {
		t.Fatalf("host turn loop = %+v want guarded", hostTurn)
	}
	if !hostTurn.HostTurn || hostTurn.Iteration != 3 || hostTurn.MaxIterations != 8 {
		t.Fatalf("host turn loop = %+v want host iteration 3/8", hostTurn)
	}

	closeout := coordinatorLLMLoopProgress("coordinator", surface.SurfaceImplementDispatch, "Starting workers", false, 6, 500, true)
	if closeout == nil || !closeout.ProseFinish {
		t.Fatalf("early closeout loop = %+v want prose_finish", closeout)
	}
	if closeout.Iteration != 7 || closeout.MaxIterations != 500 {
		t.Fatalf("early closeout iteration = %d/%d want 7/500", closeout.Iteration, closeout.MaxIterations)
	}

	if loop := coordinatorLLMLoopProgress("implementer", "", "", false, 0, 8, false); loop != nil {
		t.Fatalf("worker profile loop = %+v want nil", loop)
	}
}
