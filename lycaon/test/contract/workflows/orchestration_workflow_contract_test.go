package contract

import (
	"context"
	"os"
	"path/filepath"
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

func TestBugbashInWorkflowCatalog(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	m, ok := catalog["bugbash@1.1.0"]
	if !ok {
		t.Fatal("bugbash@1.1.0 missing from bundled catalog")
	}
	if m.Topology != "bugbash" {
		t.Fatalf("topology = %q want bugbash", m.Topology)
	}
	if !m.IsCatalogVisible() {
		t.Fatal("bugbash must be catalog-visible")
	}
	summary := m.Summary()
	if summary.Topology != "bugbash" {
		t.Fatalf("summary topology = %q", summary.Topology)
	}
	if summary.Trigger != "/bugbash" {
		t.Fatalf("summary trigger = %q want /bugbash", summary.Trigger)
	}

	resolver := workflowcatalog.Resolver{}
	summaries, err := resolver.ListResolved(context.Background(), "", "")
	contractcheck.FailErr(t, "resolver.ListResolved failed", err)
	hasBugbash, hasOptions := false, false
	for _, row := range summaries {
		if row.ID == "implement" {
			t.Fatal("implement (attach.policy session_create) must not appear in product catalog API")
		}
		switch row.ID {
		case "bugbash":
			hasBugbash = true
			if row.Trigger != "/bugbash" {
				t.Fatalf("bugbash trigger = %q want /bugbash", row.Trigger)
			}
		case "options":
			hasOptions = true
			if row.Trigger != "/options" {
				t.Fatalf("options trigger = %q want /options", row.Trigger)
			}
		}
	}
	if !hasBugbash || !hasOptions {
		t.Fatalf("product catalog missing launch workflows: bugbash=%v options=%v", hasBugbash, hasOptions)
	}
	for _, id := range []string{"plan"} {
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

func TestBundledDefaultPipelineTopologyYAMLExists(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	// default-pipeline ships as a topology file with no workflow manifest.
	topologyPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "workflows", "_topologies", "default-pipeline.yaml")
	if _, err := os.Stat(topologyPath); err != nil {
		t.Fatalf("topology file: %v", err)
	}
	bugbashPath := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "bugbash", "workflows", "bugbash", "workflow.yaml")
	m, err := workflowdef.LoadManifestFromFile(bugbashPath)
	contractcheck.FailErr(t, "workflow.LoadManifestFromFile failed", err)
	if m.Topology != "bugbash" {
		t.Fatalf("topology = %q", m.Topology)
	}
	bugbashTopo := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "workflows", "_topologies", m.Topology+".yaml")
	if _, err := os.Stat(bugbashTopo); err != nil {
		t.Fatalf("bugbash topology file: %v", err)
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
