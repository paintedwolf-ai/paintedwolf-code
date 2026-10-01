package surface

import "testing"

func TestExecutionModeFamily_implementSynthesisIsWrapup(t *testing.T) {
	if got := ExecutionModeFamily("implement_synthesis"); got != ExecutionModeFamilyWrapup {
		t.Fatalf("ExecutionModeFamily(implement_synthesis) = %q want %q", got, ExecutionModeFamilyWrapup)
	}
}

func TestExecutionModeFamily_dispatchStaysOrchestrate(t *testing.T) {
	if got := ExecutionModeFamily("implement_dispatch"); got != ExecutionModeFamilyOrchestrate {
		t.Fatalf("ExecutionModeFamily(implement_dispatch) = %q want %q", got, ExecutionModeFamilyOrchestrate)
	}
}
