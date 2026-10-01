package mcp

import (
	"context"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func generationFixtureRegistry(t *testing.T) *RegistryImpl {
	t.Helper()
	reg, err := NewRegistryImpl(RegistryOptions{Connector: &MockConnector{}})
	testutil.FailErr(t, "NewRegistryImpl", err)
	t.Cleanup(func() { _ = reg.Close() })
	reg.deviceCatalog = []MergedMCPProviderEntry{
		{MCPProviderEntry: MCPProviderEntry{ID: "tracker", Command: "/bin/true", Enabled: true}},
		{MCPProviderEntry: MCPProviderEntry{ID: "archive", Command: "/bin/true", Enabled: false}},
	}
	reg.syncOK = map[string]bool{"tracker": true}
	reg.toolDefs = map[string][]sanitizedToolDefinition{
		"tracker": {
			{Name: "search_issues", Description: "Search", Schema: map[string]any{"type": "object"}},
			{Name: "create_issue", Description: "Create", Schema: map[string]any{"type": "object"}},
		},
	}
	reg.deviceRejected = []RejectedRow{{ID: "bad", Layer: CatalogLayerUser, Reason: "invalid_entry"}}
	return reg
}

func TestCurrentGenerationCapturesCatalogAndReadiness(t *testing.T) {
	reg := generationFixtureRegistry(t)
	gen := reg.CurrentGeneration(context.Background(), CallScope{})

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
	first := reg.CurrentGeneration(context.Background(), CallScope{})
	second := reg.CurrentGeneration(context.Background(), CallScope{})
	if first.Revision != second.Revision {
		t.Fatal("unchanged registry must yield an identical revision")
	}

	reg.mu.Lock()
	reg.syncErrors = map[string]string{"tracker": CodeSyncFailed}
	reg.syncOK = map[string]bool{}
	reg.mu.Unlock()
	third := reg.CurrentGeneration(context.Background(), CallScope{})
	if third.Revision == first.Revision {
		t.Fatal("a readiness change must change the revision")
	}
	tracker, _ := third.Provider("tracker")
	if tracker.Status == api.McpStatusReady {
		t.Fatal("failed sync must not read as ready")
	}
}

func TestCurrentGenerationNilRegistry(t *testing.T) {
	var reg *RegistryImpl
	gen := reg.CurrentGeneration(context.Background(), CallScope{})
	if gen == nil || gen.Revision == "" || len(gen.Providers) != 0 {
		t.Fatalf("nil registry generation = %+v", gen)
	}
}

func TestCurrentGenerationDetachesSchemas(t *testing.T) {
	reg := generationFixtureRegistry(t)
	first := reg.CurrentGeneration(context.Background(), CallScope{})
	tracker, ok := first.Provider("tracker")
	if !ok || len(tracker.Tools) == 0 {
		t.Fatal("tracker tools missing")
	}
	tracker.Tools[0].Schema["type"] = "string"

	second := reg.CurrentGeneration(context.Background(), CallScope{})
	tracker, _ = second.Provider("tracker")
	if got := tracker.Tools[0].Schema["type"]; got != "object" {
		t.Fatalf("schema type = %v", got)
	}
}

func TestCurrentGenerationRejectsUnencodableDefinitions(t *testing.T) {
	reg := generationFixtureRegistry(t)
	reg.toolDefs["tracker"] = append(reg.toolDefs["tracker"], sanitizedToolDefinition{
		Name: "invalid", Schema: map[string]any{"default": math.NaN()},
	})
	gen := reg.CurrentGeneration(t.Context(), CallScope{})
	provider, ok := gen.Provider("tracker")
	if !ok || provider.Status != api.McpStatusError || len(provider.Tools) != 0 {
		t.Fatalf("invalid definition exposed usable tools: %+v, present=%v", provider, ok)
	}
	archive, ok := gen.Provider("archive")
	if !ok || archive.Status != api.McpStatusDisabled {
		t.Fatalf("unrelated provider changed: %+v, present=%v", archive, ok)
	}
}
