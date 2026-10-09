package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Every sealed version in the stock catalog loads as a retired definition that
// existing runs resume on and new runs cannot start.
func TestArchivedWorkflowsLoadFromCatalog(t *testing.T) {
	t.Parallel()
	catalog, err := extpacks.ResolveStockCatalog(t.Context(), nil)
	contractcheck.FailErr(t, "resolve stock catalog", err)
	manifests, sources, err := workflowdef.LoadManifestsFromCatalog(catalog)
	contractcheck.FailErr(t, "load catalog manifests", err)
	reg := workflowdef.NewRegistry(manifests)

	var sealed int
	for _, unitID := range catalog.LoadedUnitIDs() {
		archiveKey, rest, ok := extpacks.SplitArchiveUnitID(unitID)
		if !ok || rest != "workflow" {
			continue
		}
		sealed++
		var found bool
		for key, m := range manifests {
			if extpacks.ArchiveKey(m.ID, m.Version) != archiveKey {
				continue
			}
			found = true
			if sources[key].Origin != workflowdef.OriginArchive {
				t.Errorf("%s origin = %v, want archive", key, sources[key].Origin)
			}
			if !m.Retired {
				t.Errorf("%s must load retired", key)
			}
			if reg.CatalogStartable(m.ID, m.Version) {
				t.Errorf("%s is sealed and must not be startable", key)
			}
		}
		if !found {
			t.Errorf("sealed unit %s produced no manifest", unitID)
		}
	}
	if sealed == 0 {
		t.Fatal("the stock catalog seals no workflow version")
	}
}
