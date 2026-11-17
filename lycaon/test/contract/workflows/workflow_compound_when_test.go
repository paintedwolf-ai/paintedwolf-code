package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCompoundCompleteWhenValidInTestdata(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "test", "contract", "testdata", "workflows", "compound-valid.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	m, err := workflowdef.ParseManifestYAML(data)
	contractcheck.FailErr(t, "workflow.ParseManifestYAML failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDepsWithEvidence())
	contractcheck.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	if diags := vocabulary.ValidateBundled(reg, workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		m.ID + "@" + m.Version: m,
	}), nil); len(diags) > 0 {
		t.Fatalf("valid compound manifest: %v", diags)
	}
}

// The forbidden leaf alone decides the diagnostic: the same compound without it
// validates clean.
func TestCompoundCompleteWhenForbiddenLeafFailsLoad(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	validate := func(completeWhen string) []api.ComposeValidationError {
		m := workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:        "bad",
			Version:   "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{{ID: "x", CompleteWhen: completeWhen}},
		})
		return vocabulary.ValidateBundled(reg, workflowdef.NewRegistry(map[string]workflowdef.Manifest{"bad@1.0.0": m}), nil)
	}
	if diags := validate("human_approval"); len(diags) > 0 {
		t.Fatalf("control compound: %+v", diags)
	}
	diags := validate("human_approval and stage_plan_complete")
	if len(diags) != 1 || diags[0].Code != "forbidden_predicate" || diags[0].Field != "workflow[bad@1.0.0].phases[x].complete_when" {
		t.Fatalf("diagnostics = %+v want one forbidden_predicate on the complete_when", diags)
	}
}

// A host-only condition in a rule when is refused at load; a registered key is not.
func TestVarSetForbiddenInRuleWhen(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	validate := func(key string) []api.ComposeValidationError {
		cfg := &rules.RulesConfig{Rules: []rules.RuleEntry{{ID: "r", When: map[string]any{key: true}}}}
		return vocabulary.ValidateBundled(reg, nil, []*rules.RulesConfig{cfg})
	}
	if diags := validate("human_approval"); len(diags) > 0 {
		t.Fatalf("control rule: %+v", diags)
	}
	diags := validate("var_set:plan.status")
	if len(diags) == 0 {
		t.Fatal("var_set rule when validated")
	}
	for _, d := range diags {
		if d.Code != "forbidden_predicate" || d.Field != "rules[0].r.when" {
			t.Fatalf("diagnostic = %+v want forbidden_predicate on rules[0].r.when", d)
		}
	}
}
