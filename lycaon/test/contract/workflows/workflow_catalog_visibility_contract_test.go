package contract

import (
	"context"
	"testing"

	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWorkflowCatalogOmitsSystemTier(t *testing.T) {
	t.Parallel()
	resolver := workflowcatalog.Resolver{}
	summaries, err := resolver.ListResolved(context.Background(), "", "")
	contractcheck.FailErr(t, "ListResolved", err)
	for _, row := range summaries {
		if row.ID == "implement" {
			t.Fatal("attach.policy session_create implement must not appear in product catalog API")
		}
	}
	reg, _, err := resolver.Resolve(context.Background(), "", "")
	contractcheck.FailErr(t, "Resolve", err)
	impl, err := reg.Get("implement", "1.0.0")
	contractcheck.FailErr(t, "Get implement manifest", err)
	if impl.Attach.Policy != workflowdef.AttachPolicySessionCreate {
		t.Fatalf("implement attach.policy = %q want session_create", impl.Attach.Policy)
	}
	if impl.IsCatalogVisible() {
		t.Fatal("implement must not be catalog-visible")
	}
	for _, id := range []string{"plan", "bugbash", "options"} {
		found := false
		for _, row := range summaries {
			if row.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("product catalog missing %q", id)
		}
	}
}
