package batch

import "testing"

func TestApplyTransition_happyPath(t *testing.T) {
	vars := map[string]any{}
	vars, state, ok := ApplyTransition(vars, EventVisibleUserMessage, 0)
	if !ok || state.Phase != PhasePreDispatch || state.Seq != 1 {
		t.Fatalf("reset = %v ok=%v", state, ok)
	}

	seq := state.Seq
	steps := []Event{
		EventWriterTaskEnqueued,
		EventOverlaysPendingIdle,
		EventSynthesisReady,
		EventGroundedSynthesisAccepted,
	}
	wantPhases := []string{PhaseDispatch, PhaseIntegrate, PhaseSynthesize, PhaseClosed}
	for i, ev := range steps {
		vars, state, ok = ApplyTransition(vars, ev, seq)
		if !ok || state.Phase != wantPhases[i] {
			t.Fatalf("step %d event=%v state=%v ok=%v want %q", i, ev, state, ok, wantPhases[i])
		}
	}
}

func TestApplyTransition_staleSeqDropped(t *testing.T) {
	vars := map[string]any{
		"coordinator_batch": map[string]any{
			"phase": PhaseDispatch,
			"seq":   3,
		},
	}
	_, state, ok := ApplyTransition(vars, EventOverlaysPendingIdle, 2)
	if ok {
		t.Fatalf("stale seq should be dropped, got %v", state)
	}
	if state.Seq != 3 || state.Phase != PhaseDispatch {
		t.Fatalf("state mutated: %v", state)
	}
}

func TestApplyTransition_userResetIncrementsSeq(t *testing.T) {
	vars := map[string]any{
		"coordinator_batch": map[string]any{
			"phase": PhaseClosed,
			"seq":   4,
		},
	}
	_, state, ok := ApplyTransition(vars, EventVisibleUserMessage, 0)
	if !ok || state.Seq != 5 || state.Phase != PhasePreDispatch {
		t.Fatalf("reset state = %v ok=%v", state, ok)
	}
}
