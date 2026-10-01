package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A vars snapshot taken before latchIntakeKey does not observe mutations of the
// returned vars: cloneVars aliases nested maps, so latchIntakeKey deep-copies the
// user_decision bucket (cloneAskVars) before writing a new key.
func TestLatchIntakeKeyDoesNotMutateSharedNestedVars(t *testing.T) {
	sharedDecision := map[string]any{
		"other_key": map[string]any{"prompt": "existing", "pending": true},
	}
	vars := map[string]any{"user_decision": sharedDecision}

	_, err := latchIntakeKey(vars, "change_size")
	testutil.FailErr(t, "latchIntakeKey failed", err)

	if _, ok := sharedDecision["change_size"]; ok {
		t.Fatal("latchIntakeKey mutated the caller's shared user_decision map in place")
	}
	if len(sharedDecision) != 1 {
		t.Fatalf("shared user_decision bucket unexpectedly grew: %v", sharedDecision)
	}
}
