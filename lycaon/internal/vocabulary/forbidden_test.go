package vocabulary

import "testing"

func TestForbiddenConditionIDsExact(t *testing.T) {
	for id := range forbiddenConditionIDs {
		if !IsForbidden(id) {
			t.Fatalf("forbidden condition id %q not reported by IsForbidden", id)
		}
	}
}

func TestReplacementHintStageComplete(t *testing.T) {
	hint := ReplacementHint("stage_plan_complete")
	if hint == "" || hint == "see docs/workflows.md" {
		t.Fatalf("hint = %q", hint)
	}
	if want := "topology_stage_complete with bind_topology_stage: plan"; hint != want {
		t.Fatalf("hint = %q want %q", hint, want)
	}
}
