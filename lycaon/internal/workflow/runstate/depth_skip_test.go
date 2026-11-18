package runstate_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

// TestResearchDepthNoneSetsPlanCatalogPredicate drives the depth parameter
// through ResolveDepthSkip and reads the result back through the
// phase_skipped condition the plan catalog gates on.
func TestResearchDepthNoneSetsPlanCatalogPredicate(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	if err != nil {
		t.Fatalf("build conditions registry: %v", err)
	}
	for _, tc := range []struct {
		name  string
		depth any
		want  bool
	}{
		{"none skips", "none", true},
		{"light runs", "light", false},
		{"thorough runs", "thorough", false},
		{"unset runs", nil, false},
		{"unknown level runs", "skip-it", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := map[string]any{}
			if tc.depth != nil {
				vars = runstate.SetHostVar(vars, "params.research_depth", tc.depth)
			}
			vars = runstate.ResolveDepthSkip(vars, "research", "research_depth")
			got, err := reg.Evaluate("phase_skipped:research", conditions.EvalContext{Vars: vars})
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if got != tc.want {
				t.Fatalf("phase_skipped:research = %v, want %v (vars %v)", got, tc.want, vars)
			}
			other, err := reg.Evaluate("phase_skipped:plan", conditions.EvalContext{Vars: vars})
			if err != nil || other {
				t.Fatalf("phase_skipped:plan = %v err %v, want false", other, err)
			}
		})
	}
}
