package definition_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func catalogWorkflowIDs(t *testing.T) map[string]bool {
	t.Helper()
	manifests, _, err := workflowdef.LoadPackManifestsForCatalog(nil)
	testutil.FailErr(t, "loadCatalogManifestsWithSources failed", err)
	ids := map[string]bool{}
	for _, m := range manifests {
		ids[m.ID] = true
	}
	return ids
}

func TestCatalogWorkflowsHonorDisabledPack(t *testing.T) {
	defer extpacks.ClearActive()

	if !catalogWorkflowIDs(t)["bugbash"] {
		t.Fatal("precondition: bugbash must be in the effective catalog")
	}

	eff, err := extpackstest.Resolve(t.Context(), extpackstest.Disabled("painted-wolf/bugbash"), nil)
	testutil.FailErr(t, "extpacks.effective catalog resolve failed", err)
	extpacks.SetActive(eff)
	if catalogWorkflowIDs(t)["bugbash"] {
		t.Fatal("bugbash workflow must not load when painted-wolf/bugbash is disabled")
	}

	extpacks.ClearActive()
	if !catalogWorkflowIDs(t)["bugbash"] {
		t.Fatal("bugbash workflow must return once the pack is enabled again")
	}
}

func TestCatalogWorkflowsHonorDisabledUnit(t *testing.T) {
	defer extpacks.ClearActive()

	desired := extpacks.DesiredState{
		Format:   extpacks.DesiredFormat,
		Disabled: []string{extpacks.WorkflowUnitID("options")},
	}
	eff, err := extpackstest.Resolve(t.Context(), desired, nil)
	testutil.FailErr(t, "extpacks.effective catalog resolve failed", err)
	extpacks.SetActive(eff)

	ids := catalogWorkflowIDs(t)
	if ids["options"] {
		t.Fatal("options workflow must not load when workflows/options is disabled")
	}
	if !ids["plan"] {
		t.Fatal("disabling one unit must not drop unrelated workflows")
	}
}
