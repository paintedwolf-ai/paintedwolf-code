package prompts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
)

func renderRecallPartial(t *testing.T, vars map[string]any) string {
	t.Helper()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	out, err := engine.Render(context.Background(), "units/native-recall-tool.md", vars)
	testutil.FailErr(t, "render native-recall-tool unit", err)
	return out
}

// Widening is a topology fact: a root session may reach other sessions, a
// worker leg may not. The partial teaches it only where the call would succeed.
func TestRecallPartialTeachesWideningOnlyToRoot(t *testing.T) {
	root := renderRecallPartial(t, map[string]any{"profile_has_recall": true, "recall_may_widen": true})
	for _, want := range []string{
		"its worker legs, finished ones included",
		"a finished leg's envelope omits a detail the leg saw",
		"[compacted …]",
		"an earlier session on this project may already have looked",
		`widen: "project"`,
		"`scope_empty` means dispatch",
	} {
		if !strings.Contains(root, want) {
			t.Fatalf("root render missing %q:\n%s", want, root)
		}
	}

	worker := renderRecallPartial(t, map[string]any{"profile_has_recall": true, "recall_may_widen": false})
	for _, banned := range []string{"widen", "worker legs", "finished leg's envelope", "dispatch"} {
		if strings.Contains(worker, banned) {
			t.Fatalf("worker render must not teach %q:\n%s", banned, worker)
		}
	}
	for _, want := range []string{"in this leg", "[compacted …]", "`scope_empty` means go observe it"} {
		if !strings.Contains(worker, want) {
			t.Fatalf("worker render missing %q:\n%s", want, worker)
		}
	}
}

func TestRecallMayWidenFollowsRenderRole(t *testing.T) {
	coordinator := prompts.CoordinatorPolicyTemplateVars([]string{"recall", "read"}, nil)
	if v, _ := coordinator["recall_may_widen"].(bool); !v {
		t.Fatalf("coordinator vars must allow widening: %#v", coordinator["recall_may_widen"])
	}
	worker := spawn.PolicyTemplateVars(spawn.DefaultWorkerToolBudget())
	if v, ok := worker["recall_may_widen"].(bool); !ok || v {
		t.Fatalf("worker vars must deny widening: %#v", worker["recall_may_widen"])
	}
}
