package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
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

func TestCompoundCompleteWhenForbiddenLeafFailsLoad(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	m := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "bad",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "x",
			CompleteWhen: "human_approval and stage_plan_complete",
		}},
	})
	diags := vocabulary.ValidateBundled(reg, workflowdef.NewRegistry(map[string]workflowdef.Manifest{"bad@1.0.0": m}), nil)
	if len(diags) == 0 {
		t.Fatal("expected forbidden leaf in compound to fail load")
	}
}

func TestVarSetForbiddenInRuleWhen(t *testing.T) {
	t.Parallel()
	if !conditions.IsHostOnlyCondition("var_set:plan.status") {
		t.Fatal("var_set must be host-only")
	}
}
