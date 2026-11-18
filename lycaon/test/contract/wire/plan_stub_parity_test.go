package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
