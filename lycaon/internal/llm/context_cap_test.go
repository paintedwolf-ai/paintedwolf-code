package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
)

const feedOnlyModel = "feed-only-model"

// feedOnlyRegistry discovers ids without lengths, so only the feed knows the window.
func feedOnlyRegistry(t *testing.T, feedWindow int, localModels string) (*Registry, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": feedOnlyModel}}})
	}))
	t.Cleanup(server.Close)
	catalog := mustCatalogCloneShipToLocal(t, "providers:\n  - id: hosted\n    kind: openai\n    base_url: "+server.URL+"/v1\n    api_key_env: \"\"\n    models: "+localModels+"\n"+MinimalShipHTTPRetryYAML)
	creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	testutil.FailErr(t, "set key", creds.Set("hosted", "test-key"))
	reg, err := NewRegistry(t.Context(), catalog, creds)
	testutil.FailErr(t, "build registry", err)
	t.Cleanup(func() { testutil.FailErr(t, "close discovery", reg.discovery.Close(context.Background())) })
	raw, err := json.Marshal(map[string]any{"openai": map[string]any{"id": "openai", "models": map[string]any{feedOnlyModel: map[string]any{
		"id": feedOnlyModel, "tool_call": true, "modalities": map[string]any{"output": []string{"text"}}, "limit": map[string]int{"context": feedWindow},
	}}}})
	testutil.FailErr(t, "encode feed", err)
	feed, err := modelfeed.New(modelfeed.Options{
		CacheDir: filepath.Join(t.TempDir(), "modelfeed"),
		GetBytes: func(context.Context, string) ([]byte, error) { return raw, nil },
	})
	testutil.FailErr(t, "feed", err)
	_, err = feed.Refresh(t.Context())
	testutil.FailErr(t, "refresh feed", err)
	reg.SetModelFeed(t.Context(), feed)
	return reg, hits
}

func TestContextLengthReadsTheWindowTheRequestBudgetUses(t *testing.T) {
	for _, tc := range []struct {
		name         string
		local        string
		feed, window int
	}{
		{name: "small feed window", local: "[]", feed: 32768, window: 32768},
		{name: "large feed window", local: "[]", feed: 1_048_572, window: 1_048_572},
		{name: "configured limit lowers the feed", local: "[{id: " + feedOnlyModel + ", context_length: 16384}]", feed: 32768, window: 16384},
		{name: "configured limit cannot expand the feed", local: "[{id: " + feedOnlyModel + ", context_length: 65536}]", feed: 32768, window: 32768},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, hits := feedOnlyRegistry(t, tc.feed, tc.local)
			reg.RefreshDiscovery("hosted")
			before := hits.Load()
			if got := reg.ContextLength("hosted", feedOnlyModel); got != tc.window {
				t.Fatalf("ContextLength = %d, want %d", got, tc.window)
			}
			if got := hits.Load(); got != before {
				t.Fatalf("ContextLength made %d discovery calls", got-before)
			}
			req := modelcall.CompletionRequest{Model: feedOnlyModel}
			if got := reg.RequestBudget(t.Context(), "hosted", req).Window; got != tc.window {
				t.Fatalf("RequestBudget window = %d, want %d", got, tc.window)
			}
		})
	}
}

func TestContextLengthKeepsItsSourcesWithoutAPublishedRow(t *testing.T) {
	reg, _ := feedOnlyRegistry(t, 32768, "[]")
	if got := reg.ContextLength("hosted", "unlisted"); got != 0 {
		t.Fatalf("unknown model = %d, want 0", got)
	}
	if got := reg.ContextLength("absent", feedOnlyModel); got != 0 {
		t.Fatalf("unknown provider = %d, want 0", got)
	}
	r := &Registry{}
	r.snapshot.Store(&providerRegistrySnapshot{
		providers: map[string]modelcall.Provider{"bare": &loggingProvider{inner: stubProviderWithoutCatalog{}}},
		entries:   map[string]CatalogEntry{"bare": {Kind: "ollama", Models: []modelinfo.Entry{{ID: "m", ContextLength: 8192}}}},
	})
	if got := r.ContextLength("bare", "m"); got != 8192 {
		t.Fatalf("configured limit = %d, want 8192", got)
	}
}

type stubProviderWithoutCatalog struct{ modelcall.Provider }

func TestFeedOnlyWindowReachesLiveBudgetAndSummaryBudget(t *testing.T) {
	reg, _ := feedOnlyRegistry(t, 32768, "[]")
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "hosted", Model: feedOnlyModel}, Lite: ModelRef{ProviderID: "hosted", Model: feedOnlyModel}}
	window, source := ResolveTrueWindow(policy, reg, modelinfo.DefaultModelContextWindows(), compaction.DefaultCompactionConfig())
	if window != 32768 || source != WindowResolveModelEntry {
		t.Fatalf("true window = %d from %q", window, source)
	}
	store, err := NewPolicyStoreAt(filepath.Join(t.TempDir(), "policy.yaml"))
	testutil.FailErr(t, "policy store", err)
	testutil.FailErr(t, "put policy", store.PutGlobal(policy))
	summarizer := &RegistrySummarizer{Registry: reg, Policy: store}
	got := summarizer.SummaryBudget()
	if want := (compaction.SummaryBudget{Input: 22016, Output: 1536}); got != want {
		t.Fatalf("summary budget = %+v, want %+v", got, want)
	}
}
