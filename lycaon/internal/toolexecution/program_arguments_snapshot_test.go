package toolexecution

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestNativeArgumentsFreezeNestedReviewedInputsAndReleaseOnce(t *testing.T) {
	input := map[string]any{"nested": map[string]any{"list": []any{"before"}}, "argv": []string{"original"}}
	admission := stageMemory(t, 64<<20)
	snapshot, err := prepareNativeArguments(t.Context(), admission, input)
	testutil.FailErr(t, "freeze canonical inputs", err)
	input["nested"].(map[string]any)["list"].([]any)[0] = "changed"
	input["argv"].([]string)[0] = "changed"
	if snapshot.values["nested"].(map[string]any)["list"].([]any)[0] != "before" || snapshot.values["argv"].([]string)[0] != "original" || admission.InUse().State == 0 {
		t.Fatal("held review aliases caller state or has no reservation")
	}
	snapshot.close()
	snapshot.close()
	if admission.InUse().State != 0 {
		t.Fatal("retained inputs leaked reservation")
	}
}

func TestNativeArgumentsDenyBeforeCloneIncludingCyclicHostValues(t *testing.T) {
	input := map[string]any{}
	input["cycle"] = input
	memory := stageMemory(t, 1<<16)
	if _, err := prepareNativeArguments(t.Context(), memory, input); err == nil || memory.InUse().State != 0 {
		t.Fatalf("cyclic host input bypassed bounded traversal: %v held%+v", err, memory.InUse())
	}
}
