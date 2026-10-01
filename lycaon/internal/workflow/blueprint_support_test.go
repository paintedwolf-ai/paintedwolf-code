package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/blueprint"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestSupportsBlueprints(t *testing.T) {
	if workflowdef.SupportsBlueprints(workflowdef.Manifest{}) {
		t.Fatal("empty manifest must not support blueprints")
	}
	if !workflowdef.SupportsBlueprints(workflowdef.Manifest{Blueprint: &workflowdef.BlueprintDef{}}) {
		t.Fatal("blueprint block without path must support blueprints")
	}
}

func TestManifestSummarySupportsBlueprints(t *testing.T) {
	with := workflowdef.Manifest{ID: "plan", Version: "1.0.0", Name: "Plan", Blueprint: &workflowdef.BlueprintDef{}}
	sum := with.Summary()
	if !sum.SupportsBlueprints {
		t.Fatal("summary must set supports_blueprints when manifest declares blueprint")
	}
	without := workflowdef.Manifest{ID: "recon-pack", Version: "1.0.0", Name: "Recon"}
	if without.Summary().SupportsBlueprints {
		t.Fatal("summary must omit supports_blueprints when manifest has no blueprint block")
	}
}

func TestCompatibleWorkflowIDsConvention(t *testing.T) {
	manifests := []workflowdef.Manifest{
		{ID: "plan", Blueprint: &workflowdef.BlueprintDef{}},
		{ID: "options", Blueprint: &workflowdef.BlueprintDef{Path: blueprint.ConventionPath("options-selection.md")}},
		{ID: "implement"},
	}
	ids := CompatibleWorkflowIDs(blueprint.ConventionPath("foo.md"), manifests)
	if len(ids) != 2 || ids[0] != "plan" {
		t.Fatalf("ids = %#v", ids)
	}
	if CompatibleWorkflowIDs("@plan", manifests) != nil {
		t.Fatal("@plan must yield no compatible workflows")
	}
}
