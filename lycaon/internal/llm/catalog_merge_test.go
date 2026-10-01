package llm

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/modelfeed"
)

func fixtureDoc(t *testing.T) *modelfeed.Document {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "modelfeed", "testdata", "models_dev_fixture.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	doc, err := modelfeed.ParseDocument(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func TestMergeCatalogAuthoritativeEmptyDiscoveryKeepsEligible(t *testing.T) {
	doc := fixtureDoc(t)
	merged := mergeAssignableModels(
		"openai",
		nil,
		nil, // empty discovery
		nil,
		doc,
		modelfeed.StatusOK,
		true,
	)
	if !merged.CatalogAuthoritative {
		t.Fatal("expected catalog authoritative")
	}
	if merged.DiscoveryStatus != DiscoveryStatusEmpty {
		t.Fatalf("discovery_status = %q", merged.DiscoveryStatus)
	}
	ids := map[string]bool{}
	for _, m := range merged.Models {
		ids[m.ID] = true
	}
	if !ids["gpt-4.1"] || !ids["o3"] {
		t.Fatalf("expected eligible catalog kept, got %v", ids)
	}
	if ids["text-embedding-3-small"] || ids["gpt-image-1"] {
		t.Fatalf("embeds/image must not appear: %v", ids)
	}
}

func TestFeedCapabilityProjectionDoesNotInventStreaming(t *testing.T) {
	entry := modelEntryFromFeed(modelfeed.Model{
		ID:         "chat-model",
		Modalities: modelfeed.Modalities{Input: []string{"text"}, Output: []string{"text"}},
	})
	if entry.Capabilities.Streaming.State != modelinfo.CapabilityUnknown {
		t.Fatalf("streaming = %+v, want unknown", entry.Capabilities.Streaming)
	}
	if entry.Capabilities.Vision.State != modelinfo.CapabilityUnsupported {
		t.Fatalf("vision = %+v, want unsupported", entry.Capabilities.Vision)
	}
}

func TestCapabilityMergeRetainsConcordantProvenance(t *testing.T) {
	merged := modelinfo.MergeCapabilities(
		modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "models.dev")},
		modelinfo.ModelCapabilities{Tools: modelinfo.Evidence(modelinfo.CapabilitySupported, "provider-discovery")},
	)
	if len(merged.Tools.Sources) != 2 || merged.Tools.Sources[0] != "models.dev" || merged.Tools.Sources[1] != "provider-discovery" {
		t.Fatalf("sources = %v", merged.Tools.Sources)
	}
}

func TestMergeVertexExpressRequiresTypedDiscovery(t *testing.T) {
	doc := &modelfeed.Document{Providers: map[string]modelfeed.Provider{
		"google": {
			ID: "google",
			Models: map[string]modelfeed.Model{
				"gemini-2.5-flash": {
					ID:         "gemini-2.5-flash",
					ToolCall:   new(true),
					Modalities: modelfeed.Modalities{Output: []string{"text"}},
					Cost:       &modelfeed.Cost{Input: new(float64(0.3)), Output: new(float64(2.5))},
				},
				"deep-research-preview-04-2026": {
					ID:         "deep-research-preview-04-2026",
					Reasoning:  new(true),
					Modalities: modelfeed.Modalities{Output: []string{"text"}},
					Cost:       &modelfeed.Cost{Input: new(float64(1)), Output: new(float64(1))},
				},
			},
		},
	}}
	merged := mergeAssignableModels(
		"vertex-express",
		nil,
		nil,
		nil,
		doc,
		modelfeed.StatusOK,
		true,
	)
	if merged.CatalogAuthoritative {
		t.Fatal("vertex-express availability must come from its live transport, not the global feed")
	}
	if len(merged.Models) != 0 {
		t.Fatalf("models = %+v, want none without typed discovery", merged.Models)
	}

	discovered := mergeAssignableModels(
		"vertex-express",
		nil,
		[]modelinfo.Entry{
			{ID: "gemini-2.5-flash", ContextLength: 1_048_576},
			{ID: "untyped-specialty-model"},
		},
		nil,
		doc,
		modelfeed.StatusOK,
		true,
	)
	if len(discovered.Models) != 1 || discovered.Models[0].ID != "gemini-2.5-flash" {
		t.Fatalf("models = %+v, want only the live, feed-eligible model", discovered.Models)
	}
}

func TestMergeVertexUsesOnlyDiscoveredCallableIDs(t *testing.T) {
	doc := &modelfeed.Document{Providers: map[string]modelfeed.Provider{
		"google-vertex": {
			ID: "google-vertex",
			Models: map[string]modelfeed.Model{
				"gemini-2.5-pro": {
					ID:         "gemini-2.5-pro",
					ToolCall:   new(true),
					Modalities: modelfeed.Modalities{Output: []string{"text"}},
					Cost:       &modelfeed.Cost{Input: new(float64(1)), Output: new(float64(2))},
				},
			},
		},
	}}
	withoutDiscovery := mergeAssignableModels(
		"vertex", nil, nil, nil, doc, modelfeed.StatusOK, true,
	)
	if withoutDiscovery.CatalogAuthoritative || len(withoutDiscovery.Models) != 0 {
		t.Fatalf("Vertex feed-only models = %+v, authoritative=%v", withoutDiscovery.Models, withoutDiscovery.CatalogAuthoritative)
	}

	merged := mergeAssignableModels(
		"vertex",
		nil,
		[]modelinfo.Entry{{ID: "google/gemini-2.5-pro"}, {ID: "google/not-in-feed"}},
		nil,
		doc,
		modelfeed.StatusOK,
		true,
	)
	if len(merged.Models) != 1 || merged.Models[0].ID != "google/gemini-2.5-pro" {
		t.Fatalf("Vertex models = %+v, want one callable feed-backed id", merged.Models)
	}
	if merged.Models[0].PricedAs != "gemini-2.5-pro" {
		t.Fatalf("priced_as = %q", merged.Models[0].PricedAs)
	}
}

func TestMergeAzureKeepsCustomerDeploymentNames(t *testing.T) {
	doc := fixtureDoc(t)
	merged := mergeAssignableModels(
		"azure",
		[]modelinfo.Entry{{ID: "team-reasoner", PricedAs: "gpt-4.1"}},
		nil,
		nil,
		doc,
		modelfeed.StatusOK,
		true,
	)
	if merged.CatalogAuthoritative {
		t.Fatal("Azure availability must be deployment-authoritative")
	}
	if len(merged.Models) != 1 || merged.Models[0].ID != "team-reasoner" {
		t.Fatalf("models = %+v, want only the configured deployment name", merged.Models)
	}
	if merged.Models[0].PricedAs != "gpt-4.1" {
		t.Fatalf("priced_as = %q, want gpt-4.1", merged.Models[0].PricedAs)
	}
}

func TestMergeCatalogUnavailableUsesDiscovery(t *testing.T) {
	merged := mergeAssignableModels(
		"openai",
		[]modelinfo.Entry{{ID: "local-only", InputPer1K: 1}},
		[]modelinfo.Entry{{ID: "gpt-4o"}},
		nil,
		nil,
		modelfeed.StatusUnavailable,
		false,
	)
	if merged.CatalogAuthoritative {
		t.Fatal("expected discovery-authoritative when catalog unavailable")
	}
	if len(merged.Models) != 1 || merged.Models[0].ID != "gpt-4o" {
		t.Fatalf("models = %+v", merged.Models)
	}
}

func TestMergeUnmappedDiscoveryAuthoritative(t *testing.T) {
	doc := fixtureDoc(t)
	merged := mergeAssignableModels(
		"ollama",
		nil,
		[]modelinfo.Entry{{ID: "llama3.2"}},
		nil,
		doc,
		modelfeed.StatusOK,
		true,
	)
	if merged.CatalogAuthoritative {
		t.Fatal("ollama must not be catalog-authoritative")
	}
	if len(merged.Models) != 1 || merged.Models[0].ID != "llama3.2" {
		t.Fatalf("models = %+v", merged.Models)
	}
}

func TestMergeUntypedDiscoveryIntersectsEligibleCatalog(t *testing.T) {
	doc := fixtureDoc(t)
	discovered := []modelinfo.Entry{
		{ID: "gpt-4.1", Untyped: true},
		{ID: "text-embedding-3-small", Untyped: true},
		{ID: "dall-e-3", Untyped: true},
		{ID: "gpt-live-only", Untyped: true},
	}
	merged := mergeAssignableModels(
		"openai",
		nil,
		discovered,
		nil,
		doc,
		modelfeed.StatusOK,
		true,
	)
	ids := map[string]bool{}
	for _, m := range merged.Models {
		ids[m.ID] = true
	}
	if !ids["gpt-4.1"] {
		t.Fatalf("eligible catalog id from untyped intersect missing: %v", ids)
	}
	if ids["text-embedding-3-small"] || ids["dall-e-3"] || ids["gpt-live-only"] {
		t.Fatalf("untyped non-eligible ids must not appear: %v", ids)
	}
	// Catalog-only eligible ids omitted from discovery stay visible.
	if !ids["o3"] {
		t.Fatalf("eligible catalog must remain when discovery omits id: %v", ids)
	}
}

func TestMergeUntypedUnmappedRequiresLocalAllowlist(t *testing.T) {
	discovered := []modelinfo.Entry{
		{ID: "gpt-4o", Untyped: true},
		{ID: "text-embedding-3-small", Untyped: true},
	}
	empty := mergeAssignableModels("openai-compatible", nil, discovered, nil, nil, modelfeed.StatusUnavailable, false)
	if len(empty.Models) != 0 {
		t.Fatalf("untyped unmapped without allowlist must be empty: %+v", empty.Models)
	}
	allowed := mergeAssignableModels(
		"openai-compatible",
		[]modelinfo.Entry{{ID: "gpt-4o", InputPer1K: 0.01}},
		discovered,
		nil,
		nil,
		modelfeed.StatusUnavailable,
		false,
	)
	if len(allowed.Models) != 1 || allowed.Models[0].ID != "gpt-4o" {
		t.Fatalf("allowlist intersect = %+v", allowed.Models)
	}
}

func TestMergeTypedDiscoveryAppendsLiveOnly(t *testing.T) {
	doc := fixtureDoc(t)
	discovered := []modelinfo.Entry{
		{ID: "gpt-4.1"},
		{ID: "typed-live-chat"}, // typed (Untyped=false) may append
	}
	merged := mergeAssignableModels("openai", nil, discovered, nil, doc, modelfeed.StatusOK, true)
	ids := map[string]bool{}
	for _, m := range merged.Models {
		ids[m.ID] = true
	}
	if !ids["typed-live-chat"] || !ids["o3"] {
		t.Fatalf("expected typed append + catalog keep: %v", ids)
	}
}

func TestReadyToAssignExistsSlot(t *testing.T) {
	doc := RoleExclusions{
		Rules: []RoleExclusion{
			{Providers: []string{"openai"}, Models: []string{"bad-coord"}, Roles: []string{PolicySlotCoordinator}},
			{Providers: []string{"openai"}, Models: []string{"bad-sum"}, Roles: []string{PolicySlotLite}},
		},
	}
	models := []modelinfo.Entry{{ID: "bad-coord"}, {ID: "ok"}}
	if !ReadyToAssignExistsSlot(true, "openai", models, doc) {
		t.Fatal("ok model should satisfy exists-slot")
	}
	both := []modelinfo.Entry{{ID: "bad-coord"}, {ID: "bad-sum"}}
	// bad-coord fits summarizer; bad-sum fits coordinator — still ready
	if !ReadyToAssignExistsSlot(true, "openai", both, doc) {
		t.Fatal("models excluded from only one list still count")
	}
	dual := RoleExclusions{
		Rules: []RoleExclusion{{Providers: []string{"openai"}, Models: []string{"gone"}, Roles: []string{PolicySlotCoordinator, PolicySlotLite}}},
	}
	if ReadyToAssignExistsSlot(true, "openai", []modelinfo.Entry{{ID: "gone"}}, dual) {
		t.Fatal("model on both lists must not make provider ready")
	}
	if ReadyToAssignExistsSlot(false, "openai", []modelinfo.Entry{{ID: "ok"}}, doc) {
		t.Fatal("unconfigured must not be ready")
	}
}

func TestModelEntryFromFeedCarriesUsableOutputLimit(t *testing.T) {
	cases := []struct {
		name  string
		limit *modelfeed.Limit
		want  int
	}{
		{name: "no limit block", limit: nil, want: 0},
		{name: "output below context", limit: &modelfeed.Limit{Context: 128_000, Output: 8_192}, want: 8_192},
		{name: "output without context", limit: &modelfeed.Limit{Output: 4_096}, want: 4_096},
		{name: "output equal to context leaves no prompt room", limit: &modelfeed.Limit{Context: 500_000, Output: 500_000}, want: 0},
		{name: "output above context", limit: &modelfeed.Limit{Context: 1_000, Output: 2_000}, want: 0},
		{name: "zero output", limit: &modelfeed.Limit{Context: 128_000}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := modelEntryFromFeed(modelfeed.Model{ID: "m", Limit: tc.limit})
			if entry.MaxTokens != tc.want {
				t.Fatalf("MaxTokens = %d, want %d", entry.MaxTokens, tc.want)
			}
		})
	}
}

func TestDiscoveredOutputLimitOutranksFeed(t *testing.T) {
	feed := modelEntryFromFeed(modelfeed.Model{ID: "m", Limit: &modelfeed.Limit{Context: 200_000, Output: 8_192}})
	enriched := enrichFromDiscovery(feed, modelinfo.Entry{ID: "m", MaxTokens: 32_000})
	if enriched.MaxTokens != 32_000 {
		t.Fatalf("MaxTokens = %d, want the live transport's 32000", enriched.MaxTokens)
	}
	kept := enrichFromDiscovery(feed, modelinfo.Entry{ID: "m"})
	if kept.MaxTokens != 8_192 {
		t.Fatalf("MaxTokens = %d, want the feed's 8192 when discovery states none", kept.MaxTokens)
	}
}
