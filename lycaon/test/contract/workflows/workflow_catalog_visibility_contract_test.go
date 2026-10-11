package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
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

func TestWorkflowRegistryDefaultAmbient(t *testing.T) {
	t.Parallel()
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	contractcheck.FailErr(t, "LoadRegistryConfig", err)
	if ref.ID != "implement" || ref.Version != "1.0.0" {
		t.Fatalf("default_ambient_workflow = %+v want implement@1.0.0", ref)
	}
}

func TestWorkflowAmbientAttachBijection(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog", err)
	ref, err := workflowdef.LoadRegistryConfig(extpacks.Bundled(config.PlatformFlows))
	contractcheck.FailErr(t, "LoadRegistryConfig", err)
	if err := workflowdef.ValidateAmbientAttachBijection(catalog, ref); err != nil {
		t.Fatalf("ValidateAmbientAttachBijection: %v", err)
	}
	impl, ok := catalog["implement@1.0.0"]
	if !ok {
		t.Fatal("missing implement@1.0.0")
	}
	if impl.Attach.Policy != workflowdef.AttachPolicySessionCreate {
		t.Fatalf("implement attach.policy = %q", impl.Attach.Policy)
	}
	sessionCreate := 0
	for _, m := range catalog {
		if m.Attach.Policy == workflowdef.AttachPolicySessionCreate {
			sessionCreate++
			if m.ID != "implement" || m.Version != "1.0.0" {
				t.Fatalf("unexpected session_create manifest %s@%s", m.ID, m.Version)
			}
		}
	}
	if sessionCreate != 1 {
		t.Fatalf("session_create count = %d want 1", sessionCreate)
	}

	resolver := workflowcatalog.Resolver{}
	summaries, err := resolver.ListResolved(context.Background(), "", "")
	contractcheck.FailErr(t, "ListResolved", err)
	for _, row := range summaries {
		if row.ID == "implement" {
			t.Fatal("ListResolved must exclude implement")
		}
	}
}

func TestWorkflowRunParentRunIDWireSync(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	if err := wirespec.SyncDTOFields(root, wirespec.DtoSyncSpec{
		GoValue:       api.WorkflowRun{},
		OpenAPISchema: "WorkflowRun",
		TSInterface:   "WorkflowRun",
	}); err != nil {
		t.Fatal(err)
	}
}
