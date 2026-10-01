package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPlanStubValidRuleGateParity(t *testing.T) {
	t.Parallel()
	fixtures := []struct {
		name string
		body string
	}{
		{"valid stub", conditions.TestPlanContentWithTasks},
		{"missing title", "---\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan implementation scope\n\n**Size:** small\n\n## Plan breaking changes\n\nnone\n\n## Approach\n\nship it\n"},
		{"missing research depth", "## Goal\n\nx\n\n## Assumptions\n\nx\n"},
		{"missing scope", "---\nresearch_depth: none\n---\n## Goal\n\nx\n\n## Assumptions\n\nx\n\n## Plan breaking changes\n\nnone\n"},
		{"empty", ""},
		{"approach only", "## Approach\n\nship it\n"},
	}
	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := tc.body
			reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
				BlueprintGet: func(_ context.Context, _ string) (*api.Blueprint, error) {
					return &api.Blueprint{Path: "p1", Content: body}, nil
				},
			})
			contractcheck.FailErr(t, "build conditions registry", err)
			ec := conditions.EvalContext{Ctx: context.Background(), BlueprintPath: "p1"}
			gateOK, err := reg.Evaluate("plan_stub_valid", ec)
			contractcheck.FailErr(t, "reg.Evaluate failed", err)
			ruleOK := rules.StubValidator{Content: body}.Valid()
			if gateOK != ruleOK {
				t.Fatalf("plan_stub_valid=%v stub_valid=%v", gateOK, ruleOK)
			}
			if conditions.PlanStubValidText(body) != ruleOK {
				t.Fatalf("PlanStubValidText disagrees with stub_valid")
			}
		})
	}
}

// The blueprint's research_depth and the workflow depth parameter are one
// vocabulary answered by two actors — the agent in the stub, the run in its
// parameters — so the words must stay identical.
func TestPlanDepthVocabularyMatchesWorkflowDepth(t *testing.T) {
	t.Parallel()
	fields, err := conditions.PlanBlueprintFields()
	contractcheck.FailErr(t, "PlanBlueprintFields", err)
	var field conditions.PlanField
	for _, f := range fields {
		if f.Key == conditions.ResearchDepthFieldKey {
			field = f
		}
	}
	if field.Key == "" {
		t.Fatalf("%s is not declared", conditions.ResearchDepthFieldKey)
	}
	for _, id := range field.ValueIDs() {
		level, err := workflowdef.ParseDepthLevel(id)
		if err != nil || string(level) != id {
			t.Errorf("blueprint depth %q is not a workflow depth level (%v)", id, err)
		}
	}
	if len(field.ValueIDs()) != 3 {
		t.Errorf("blueprint depth vocabulary = %v, want the three workflow levels", field.ValueIDs())
	}
	// `none` is the level that skips the phase on both sides.
	value, ok := field.Lookup(string(workflowdef.DepthNone))
	if !ok || value.OpensResearch {
		t.Errorf("%q must close the research gate, matching ResolveDepthSkip", workflowdef.DepthNone)
	}
}
