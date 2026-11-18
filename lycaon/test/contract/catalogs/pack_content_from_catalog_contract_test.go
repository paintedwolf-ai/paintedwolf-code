package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/guidance/feedback"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Disabled gate feedback is absent from the resolved catalog.
func TestGateFeedbackLoadsFromCatalogUnits(t *testing.T) {
	t.Parallel()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "DiscoverStockContent", err)

	full := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: extpacks.EmptyDesired()})
	catalog, err := feedback.LoadGateFeedbackCatalogWithCatalog(full)
	contractcheck.FailErr(t, "LoadGateFeedbackCatalogWithCatalog", err)
	ids := catalog.GateIDs()
	if len(ids) == 0 {
		t.Fatal("no gate feedback definitions in the stock catalog")
	}
	unitID := extpacks.GateFeedbackUnitIDPrefix + ids[0]
	if !full.HasLoaded(unitID) {
		t.Fatalf("gate feedback id %q has no provide unit %q — its bytes are outside the catalog revision", ids[0], unitID)
	}

	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{unitID}
	reduced := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: desired})
	after, err := feedback.LoadGateFeedbackCatalogWithCatalog(reduced)
	contractcheck.FailErr(t, "LoadGateFeedbackCatalogWithCatalog disabled", err)
	if after.Has(ids[0]) {
		t.Fatalf("disabled gate feedback %q still loaded — the loader is reading pack directories", ids[0])
	}
}

// Workflow manifests and templates come from the same unit algebra.
func TestWorkflowManifestsAndTemplatesLoadFromCatalogUnits(t *testing.T) {
	t.Parallel()
	stock, err := extpacks.DiscoverStockContent()
	contractcheck.FailErr(t, "DiscoverStockContent", err)
	full := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: extpacks.EmptyDesired()})

	manifests, _, err := workflowdef.LoadManifestsFromCatalog(full)
	contractcheck.FailErr(t, "LoadManifestsFromCatalog", err)
	if len(manifests) == 0 {
		t.Fatal("no workflow manifests in the stock catalog")
	}
	templates, err := workflowcomposition.LoadTemplatesEffective(full)
	contractcheck.FailErr(t, "LoadTemplatesEffective", err)
	if len(templates) == 0 {
		t.Fatal("no workflow templates in the stock catalog")
	}

	// The template's unit id is its file stem, which need not equal its body id,
	// so take the id the catalog published rather than deriving one.
	var templateUnitID string
	for _, id := range full.LoadedUnitIDs() {
		if strings.HasPrefix(id, extpacks.WorkflowTemplateUnitIDPrefix) {
			templateUnitID = id
			break
		}
	}
	if templateUnitID == "" {
		t.Fatal("no workflow template provide units in the stock catalog")
	}
	desired := extpacks.EmptyDesired()
	desired.Disabled = []string{extpacks.WorkflowUnitID("plan"), templateUnitID}
	reduced := extpacks.Resolve(t.Context(), extpacks.ResolveInput{Packs: stock, Desired: desired})

	afterManifests, _, err := workflowdef.LoadManifestsFromCatalog(reduced)
	contractcheck.FailErr(t, "LoadManifestsFromCatalog disabled", err)
	if len(afterManifests) >= len(manifests) {
		t.Fatalf("disabling workflows/plan kept %d manifests (was %d) — the loader is reading pack directories",
			len(afterManifests), len(manifests))
	}
	afterTemplates, err := workflowcomposition.LoadTemplatesEffective(reduced)
	contractcheck.FailErr(t, "LoadTemplatesEffective disabled", err)
	if len(afterTemplates) >= len(templates) {
		t.Fatalf("disabling a template unit kept %d templates (was %d) — the loader is reading pack directories",
			len(afterTemplates), len(templates))
	}
}

// A nil catalog resolves committed device state; it never means "load everything".
func TestContributionLoadersRefuseNilCatalog(t *testing.T) {
	t.Parallel()
	if _, err := feedback.LoadGateFeedbackCatalogWithCatalog(nil); err == nil {
		t.Error("LoadGateFeedbackCatalogWithCatalog(nil) must fail closed")
	}
	if _, _, err := workflowdef.LoadManifestsFromCatalog(nil); err == nil {
		t.Error("LoadManifestsFromCatalog(nil) must fail closed")
	}
	if _, err := workflowcomposition.LoadTemplatesEffective(nil); err == nil {
		t.Error("LoadTemplatesEffective(nil) must fail closed")
	}
}
