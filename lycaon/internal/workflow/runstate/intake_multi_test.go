package runstate_test

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"
)

// A vars snapshot taken before runstate.LatchIntakeKey does not observe mutations of the
// returned vars: runstate.CloneVars aliases nested maps, so runstate.LatchIntakeKey deep-copies the
// user_decision bucket (runstate.CloneAskVars) before writing a new key.
func TestLatchIntakeKeyDoesNotMutateSharedNestedVars(t *testing.T) {
	sharedDecision := map[string]any{
		"other_key": map[string]any{"prompt": "existing", "pending": true},
	}
	vars := map[string]any{"user_decision": sharedDecision}

	_, err := runstate.LatchIntakeKey(vars, "change_size")
	testutil.FailErr(t, "runstate.LatchIntakeKey failed", err)

	if _, ok := sharedDecision["change_size"]; ok {
		t.Fatal("runstate.LatchIntakeKey mutated the caller's shared user_decision map in place")
	}
	if len(sharedDecision) != 1 {
		t.Fatalf("shared user_decision bucket unexpectedly grew: %v", sharedDecision)
	}
}
