package catalog_test

import (
	"testing"

	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFilterProductCatalogSummaries(t *testing.T) {
	in := []api.WorkflowSummary{
		{ID: "plan", Version: "1.0.0", Scope: api.WorkflowScopeBundled},
		{ID: "implement", Version: "1.0.0", Scope: api.WorkflowScopeBundled},
		{ID: "bugbash", Version: "1.0.0", Scope: api.WorkflowScopeBundled},
		{ID: "custom", Version: "1.0.0", Scope: api.WorkflowScopeSession},
	}
	manifests := map[string]workflowdef.Manifest{
		workflowdef.ManifestKey("plan", "1.0.0"):      {ID: "plan", Version: "1.0.0"},
		workflowdef.ManifestKey("implement", "1.0.0"): {ID: "implement", Version: "1.0.0", Attach: workflowdef.ManifestAttach{Policy: workflowdef.AttachPolicySessionCreate}},
		workflowdef.ManifestKey("bugbash", "1.0.0"):   {ID: "bugbash", Version: "1.0.0"},
	}
	out := workflowcatalog.FilterProductCatalogSummaries(in, manifests)
	if len(out) != 3 {
		t.Fatalf("len = %d want 3 (plan, bugbash, session custom)", len(out))
	}
	ids := map[string]bool{}
	for _, row := range out {
		ids[row.ID] = true
		if row.Scope == api.WorkflowScopeBundled {
			m, ok := manifests[workflowdef.ManifestKey(row.ID, row.Version)]
			if !ok || !m.IsCatalogVisible() {
				t.Fatalf("unexpected bundled row %q", row.ID)
			}
		}
	}
	for _, want := range []string{"plan", "bugbash", "custom"} {
		if !ids[want] {
			t.Fatalf("missing %q in %v", want, out)
		}
	}
	if ids["implement"] {
		t.Fatal("attach.policy session_create implement must not appear in product catalog")
	}
}

func TestRegistryCatalogStartable(t *testing.T) {
	reg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		workflowdef.ManifestKey("plan", "1.0.0"):      {ID: "plan", Version: "1.0.0"},
		workflowdef.ManifestKey("implement", "1.0.0"): {ID: "implement", Version: "1.0.0", Attach: workflowdef.ManifestAttach{Policy: workflowdef.AttachPolicySessionCreate}},
		workflowdef.ManifestKey("bugbash", "1.0.0"):   {ID: "bugbash", Version: "1.0.0"},
	})
	if !reg.CatalogStartable("plan", "1.0.0") {
		t.Fatal("plan should be catalog-startable")
	}
	if !reg.CatalogStartable("bugbash", "1.0.0") {
		t.Fatal("bugbash should be catalog-startable")
	}
	if reg.CatalogStartable("implement", "1.0.0") {
		t.Fatal("implement must not be catalog-startable")
	}
}
