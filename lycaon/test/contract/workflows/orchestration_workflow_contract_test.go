package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
)

func TestWorkflowManifestTopologyFieldSync(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	if err := wirespec.SyncDTOFields(root, wirespec.DtoSyncSpec{
		GoValue:       api.WorkflowSummary{},
		OpenAPISchema: "WorkflowSummary",
		TSInterface:   "WorkflowSummary",
	}); err != nil {
		t.Fatal(err)
	}
	if err := wirespec.SyncDTOFields(root, wirespec.DtoSyncSpec{
		GoValue:       api.WorkflowPresetSummary{},
		OpenAPISchema: "WorkflowPresetSummary",
		TSInterface:   "WorkflowPresetSummary",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProductCatalogDemoSKUs(t *testing.T) {
	t.Parallel()
	resolver := workflowcatalog.Resolver{}
	summaries, err := resolver.ListResolved(context.Background(), "", "")
	contractcheck.FailErr(t, "resolver.ListResolved failed", err)

	want := map[string]string{
		"plan":            "/plan",
		"security-survey": "/security-survey",
		"recon-pack":      "/recon",
		"bugbash":         "/bugbash",
		"options":         "/options",
	}
	seen := map[string]bool{}
	for _, row := range summaries {
		trigger, ok := want[row.ID]
		if !ok {
			continue
		}
		seen[row.ID] = true
		if row.Trigger != trigger {
			t.Fatalf("%s trigger = %q want %q", row.ID, row.Trigger, trigger)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Fatalf("product catalog missing demo SKU %q", id)
		}
	}

	manifests, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	reg := workflowdef.NewRegistry(manifests)
	for trigger, wantID := range map[string]string{
		"/plan":            "plan",
		"/security-survey": "security-survey",
		"/recon":           "recon-pack",
		"/bugbash":         "bugbash",
		"/options":         "options",
	} {
		match, ok := reg.FindTriggerMatch(trigger)
		if !ok {
			t.Fatalf("FindTriggerMatch(%q) not found", trigger)
		}
		if match.Manifest.ID != wantID {
			t.Fatalf("FindTriggerMatch(%q) manifest = %q want %q", trigger, match.Manifest.ID, wantID)
		}
	}
}

func TestOrchestratorImplRegistered(t *testing.T) {
	t.Parallel()
	agents := orchestration.NewMemoryAgentRegistry()
	contractcheck.FailErr(t, "LoadRequiredAgentRegistry", orchestration.LoadRequiredAgentRegistry(context.Background(), agents))
	orch := orchestration.NewOrchestratorImpl(orchestration.OrchestratorDeps{
		Agents:  agents,
		Catalog: extpacks.CatalogForConsumers,
	})
	if orch == nil {
		t.Fatal("NewOrchestratorImpl returned nil")
	}
	spec, err := orch.LoadTopology(context.Background(), extpacks.Bundled(config.PlatformFlows.Join("_topologies", "default-pipeline.yaml")))
	contractcheck.FailErr(t, "orch.LoadTopology failed", err)
	if spec.Pattern != orchestration.TopologyPipeline {
		t.Fatalf("pattern = %q", spec.Pattern)
	}
}
