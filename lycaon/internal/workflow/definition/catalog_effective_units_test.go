package definition_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/extpackstest"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func catalogWorkflowIDs(t *testing.T, catalog *extpacks.EffectiveCatalog) map[string]bool {
	t.Helper()
	manifests, _, err := workflowdef.LoadPackManifestsForCatalog(catalog)
	testutil.FailErr(t, "loadCatalogManifestsWithSources failed", err)
	ids := map[string]bool{}
	for _, m := range manifests {
		ids[m.ID] = true
	}
	return ids
}

func TestCatalogWorkflowsHonorDisabledPack(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	stock, err := extpackstest.Resolve(t.Context(), extpacks.EmptyDesired(), nil)
	testutil.FailErr(t, "resolve enabled catalog", err)

	if !catalogWorkflowIDs(t, stock)["bugbash"] {
		t.Fatal("precondition: bugbash must be in the effective catalog")
	}

	eff, err := extpackstest.Resolve(t.Context(), extpackstest.Disabled("painted-wolf/bugbash"), nil)
	testutil.FailErr(t, "extpacks.effective catalog resolve failed", err)
	if catalogWorkflowIDs(t, eff)["bugbash"] {
		t.Fatal("bugbash workflow must not load when painted-wolf/bugbash is disabled")
	}

	if !catalogWorkflowIDs(t, stock)["bugbash"] {
		t.Fatal("bugbash workflow must return once the pack is enabled again")
	}
}

func TestCatalogWorkflowsHonorDisabledUnit(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	desired := extpacks.DesiredState{
		Format:   extpacks.DesiredFormat,
		Disabled: []string{"workflows/options"},
	}
	eff, err := extpackstest.Resolve(t.Context(), desired, nil)
	testutil.FailErr(t, "extpacks.effective catalog resolve failed", err)

	ids := catalogWorkflowIDs(t, eff)
	if ids["options"] {
		t.Fatal("options workflow must not load when workflows/options is disabled")
	}
	if !ids["plan"] {
		t.Fatal("disabling one unit must not drop unrelated workflows")
	}
}
