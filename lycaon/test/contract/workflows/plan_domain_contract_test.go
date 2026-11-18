package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPlanDomainIDsRegisteredOnBoot(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	for _, id := range conditions.ShippedPlanDomainIDs() {
		if !reg.Has(id) {
			t.Fatalf("shipped plan domain id %q not registered", id)
		}
	}
}

func TestVocabularyExportPlanCatalogStatus(t *testing.T) {
	t.Parallel()
	seen := map[string]vocabulary.CatalogEntry{}
	for _, row := range vocabulary.ExportCatalog() {
		if row.Domain == "plan" {
			seen[row.ID] = row
		}
	}
	for _, id := range conditions.ShippedPlanDomainIDs() {
		row, ok := seen[id]
		if !ok {
			t.Fatalf("missing plan domain catalog row for %q", id)
		}
		if row.Status != "shipped" || row.Layer != "domain" {
			t.Fatalf("plan domain %q catalog = %+v", id, row)
		}
	}
}

func TestValidateBundledAcceptsParameterizedPhasePredicate(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	manifests := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"phase-skip@1.0.0": workflowdef.FinalizeManifest(workflowdef.Manifest{
			ID:      "phase-skip",
			Version: "1.0.0",
			PhaseDefs: []workflowdef.PhaseDef{
				{ID: "research", CompleteWhen: "phase_skipped:research"},
			},
		}),
	})
	if diags := vocabulary.ValidateBundled(reg, manifests, nil); len(diags) > 0 {
		t.Fatalf("parameterized phase predicate should validate: %v", diags)
	}
}
