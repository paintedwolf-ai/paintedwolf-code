package mcp

import (
	"context"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func generationFixtureRegistry(t *testing.T) *Runtime {
	t.Helper()
	reg, err := NewRuntime(RuntimeOptions{Connector: &MockConnector{}})
	testutil.FailErr(t, "NewRuntime", err)
	t.Cleanup(func() { _ = reg.Close(t.Context()) })
	reg.Catalog.deviceCatalog = []MergedMCPProviderEntry{
		{MCPProviderEntry: MCPProviderEntry{ID: "tracker", Command: "/bin/true", Enabled: true}},
		{MCPProviderEntry: MCPProviderEntry{ID: "archive", Command: "/bin/true", Enabled: false}},
	}
	reg.Tools.syncOK = map[string]bool{"tracker": true}
	reg.Tools.toolDefs = map[string][]sanitizedToolDefinition{
		"tracker": {
			{Name: "search_issues", Description: "Search", Schema: map[string]any{"type": "object"}},
			{Name: "create_issue", Description: "Create", Schema: map[string]any{"type": "object"}},
		},
	}
	reg.Catalog.deviceRejected = []RejectedRow{{ID: "bad", Layer: CatalogLayerUser, Reason: "invalid_entry"}}
	return reg
}

func TestCurrentGenerationCapturesCatalogAndReadiness(t *testing.T) {
	reg := generationFixtureRegistry(t)
	gen := reg.Catalog.CurrentGeneration(context.Background(), CallScope{})

	if gen.Revision == "" {
		t.Fatal("generation must carry a revision")
	}
	tracker, ok := gen.Provider("tracker")
	if !ok || tracker.Status != api.McpStatusReady || !tracker.Enabled {
		t.Fatalf("tracker = %+v ok=%v", tracker, ok)
	}
	if len(tracker.Tools) != 2 || tracker.Tools[0].Name != "create_issue" {
		t.Fatalf("tools must sort by name: %+v", tracker.Tools)
	}
	if tracker.Tools[0].Fingerprint == "" {
		t.Fatal("captured tools must carry definition fingerprints")
	}
	archive, ok := gen.Provider("archive")
	if !ok || archive.Status != api.McpStatusDisabled {
		t.Fatalf("archive = %+v ok=%v", archive, ok)
	}
	if len(gen.Rejected) != 1 || gen.Rejected[0].Reason != "invalid_entry" {
		t.Fatalf("rejected = %+v", gen.Rejected)
	}
	if _, ok := gen.Tool("tracker", "search_issues"); !ok {
		t.Fatal("Tool accessor must resolve a captured definition")
	}
	if _, ok := gen.Tool("tracker", "delete_repo"); ok {
		t.Fatal("Tool accessor must miss an undeclared tool")
	}
}

func TestGenerationRevisionIsDeterministicAndStateSensitive(t *testing.T) {
	reg := generationFixtureRegistry(t)
	first := reg.Catalog.CurrentGeneration(context.Background(), CallScope{})
	second := reg.Catalog.CurrentGeneration(context.Background(), CallScope{})
	if first.Revision != second.Revision {
		t.Fatal("unchanged registry must yield an identical revision")
	}

	reg.Tools.mu.Lock()
	reg.Tools.syncErrors = map[string]string{"tracker": CodeSyncFailed}
	reg.Tools.syncOK = map[string]bool{}
	reg.Tools.mu.Unlock()
	third := reg.Catalog.CurrentGeneration(context.Background(), CallScope{})
	if third.Revision == first.Revision {
		t.Fatal("a readiness change must change the revision")
	}
	tracker, _ := third.Provider("tracker")
	if tracker.Status == api.McpStatusReady {
		t.Fatal("failed sync must not read as ready")
	}
}

func TestCurrentGenerationNilCatalog(t *testing.T) {
	var catalog *ProviderCatalog
	gen := catalog.CurrentGeneration(context.Background(), CallScope{})
	if gen == nil || gen.Revision == "" || len(gen.Providers) != 0 {
		t.Fatalf("nil catalog generation = %+v", gen)
	}
}

func TestCurrentGenerationDetachesSchemas(t *testing.T) {
	reg := generationFixtureRegistry(t)
	first := reg.Catalog.CurrentGeneration(context.Background(), CallScope{})
	tracker, ok := first.Provider("tracker")
	if !ok || len(tracker.Tools) == 0 {
		t.Fatal("tracker tools missing")
	}
	tracker.Tools[0].Schema["type"] = "string"

	second := reg.Catalog.CurrentGeneration(context.Background(), CallScope{})
	tracker, _ = second.Provider("tracker")
	if got := tracker.Tools[0].Schema["type"]; got != "object" {
		t.Fatalf("schema type = %v", got)
	}
}

func TestCurrentGenerationRejectsUnencodableDefinitions(t *testing.T) {
	reg := generationFixtureRegistry(t)
	reg.Tools.toolDefs["tracker"] = append(reg.Tools.toolDefs["tracker"], sanitizedToolDefinition{
		Name: "invalid", Schema: map[string]any{"default": math.NaN()},
	})
	gen := reg.Catalog.CurrentGeneration(t.Context(), CallScope{})
	provider, ok := gen.Provider("tracker")
	if !ok || provider.Status != api.McpStatusError || len(provider.Tools) != 0 {
		t.Fatalf("invalid definition exposed usable tools: %+v, present=%v", provider, ok)
	}
	archive, ok := gen.Provider("archive")
	if !ok || archive.Status != api.McpStatusDisabled {
		t.Fatalf("unrelated provider changed: %+v, present=%v", archive, ok)
	}
}
