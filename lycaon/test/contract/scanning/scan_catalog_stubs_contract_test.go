package contract

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestDeferredScanCatalogStubsNotInBundledWorkflows(t *testing.T) {
	t.Parallel()
	registry, err := workflowdef.RegistryFromDirs("")
	contractcheck.FailErr(t, "load bundled workflows", err)
	reserved := vocabulary.DomainIDs("scan", "catalog")
	for key, manifest := range registry.All() {
		for _, phase := range manifest.PhaseDefs {
			for _, field := range phase.PredicateFields() {
				contractcheck.FailErr(t, "read phase predicates", field.Err)
				for _, leaf := range field.Leaves {
					if slices.Contains(reserved, leaf) {
						t.Errorf("%s phase %s %s references reserved scan predicate %q", key, phase.ID, field.Path, leaf)
					}
				}
			}
		}
	}
}

func TestImplementedScanDomainIDsRegisteredOnBoot(t *testing.T) {
	t.Parallel()
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	for _, id := range vocabulary.DomainIDs("scan", "shipped") {
		if !reg.Has(id) {
			t.Fatalf("shipped scan domain id %q not registered", id)
		}
	}
}
