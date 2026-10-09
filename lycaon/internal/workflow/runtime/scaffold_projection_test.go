package runtime

import (
	"testing"
)

func TestScaffoldProjectionUsesCanonicalHostKeys(t *testing.T) {
	vars := map[string]any{
		"host.workflow_compose_summary_id": "unselected@1.0.0",
		"host.last_failed_leaves":          []string{"invented_gate"},
	}
	if value, ok := hostStringVar(vars, "workflow_compose_summary_id"); ok || value != "" {
		t.Fatalf("unselected summary became authoritative: %q, %v", value, ok)
	}
	if got := hostStringSliceVar(vars, "last_failed_leaves"); len(got) != 0 {
		t.Fatalf("unrecorded failed gates became authoritative: %v", got)
	}
	vars["workflow_compose_summary_id"] = "selected@1.0.0"
	vars["last_failed_leaves"] = []string{"recorded_gate"}
	if value, ok := hostStringVar(vars, "workflow_compose_summary_id"); !ok || value != "selected@1.0.0" {
		t.Fatalf("selected summary = %q, %v", value, ok)
	}
	if got := hostStringSliceVar(vars, "last_failed_leaves"); len(got) != 1 || got[0] != "recorded_gate" {
		t.Fatalf("recorded gates = %v", got)
	}
}
