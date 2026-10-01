package contract

import (
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestPromptBudgetCatalogMatchesRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	reg, err := LoadPromptBudgetRegistry(lycaonRoot)
	contractcheck.FailErr(t, "LoadPromptBudgetRegistry", err)
	catalog := buildPromptBudgetCatalog(reg)

	assertCatalogHas := func(category string, ids ...string) {
		t.Helper()
		entries, ok := catalog[category]
		if !ok {
			t.Fatalf("buildPromptBudgetCatalog missing category %q", category)
		}
		for _, id := range ids {
			if _, ok := entries[id]; !ok {
				t.Fatalf("buildPromptBudgetCatalog missing %s/%s", category, id)
			}
		}
	}

	assertCatalogHas("worker_personas", reg.WorkerPersonaIDs()...)
	for _, row := range reg.Tripartite {
		assertCatalogHas("coordinator_tripartite", row.Name)
	}
	for _, spec := range reg.Injects {
		assertCatalogHas("coordinator_injects", spec.ID)
	}
	for id := range reg.AgentTemplates {
		assertCatalogHas("agent_templates", id)
	}
	assertCatalogHas("kicks", reg.KickIDs...)
}
