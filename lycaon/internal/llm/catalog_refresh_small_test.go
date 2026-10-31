package llm

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/testutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
)

func TestKnownModelMetadataDoesNotWaitForCatalogRefresh(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if calls.Add(1) == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "known"}}})
			return
		}
		once.Do(func() { close(started) })
		select {
		case <-release:
			_, _ = w.Write([]byte(`{"data":[]}`))
		case <-req.Context().Done():
		}
	}))
	t.Cleanup(server.Close)
	yaml := "providers:\n  - id: local\n    kind: openai-compatible\n    base_url: " + server.URL + "/v1\n    api_key_env: \"\"\n    models:\n      - id: known\n        capabilities:\n          vision:\n            state: supported\n            sources: [local-config]\n" + MinimalShipHTTPRetryYAML
	catalog := mustCatalogCloneShipToLocal(t, yaml)
	reg, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
	if err == nil {
		reg.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "build registry", err)
	t.Cleanup(func() { testutil.FailErr(t, "close discovery", reg.discovery.Close(context.Background())) })
	entry, _ := catalog.Get("local")
	key := discoveryCacheKey(entry.ID, providerprofile.OpenAI(), entry.BaseURL, "")
	cached, fresh, cacheErr := reg.discovery.Get(key)
	testutil.FailErr(t, "read initial metadata", cacheErr)
	if !fresh {
		t.Fatal("initial metadata was not cached")
	}
	testutil.FailErr(t, "close initial discovery", reg.discovery.Close(t.Context()))
	now := time.Now()
	reg.discovery = discovery.NewCache(func() time.Time { return now })
	reg.discovery.Put(key, cached, nil)
	now = now.Add(2 * discovery.CacheTTL)
	ctx := t.Context()
	if !reg.ModelHasVision(ctx, "local", "known") {
		t.Fatal("known capability waited for or was lost during refresh")
	}
	testutil.FailErr(t, "known capability context", ctx.Err())
	<-started
	for range 3 {
		if !reg.ModelHasVision(ctx, "local", "known") {
			t.Fatal("refresh hid existing model capabilities")
		}
		_, _ = reg.DiscoveredRate("local", "known")
		_ = reg.PricedAsModelID("local", "known")
	}
	if calls.Load() != 2 {
		t.Fatalf("discovery calls=%d, want initial load and one refresh", calls.Load())
	}
	unknownCtx, cancelUnknown := context.WithDeadline(t.Context(), time.Now().Add(-time.Millisecond))
	defer cancelUnknown()
	unknown := reg.resolveSelectedModel(unknownCtx, "local", "unknown")
	if !errors.Is(unknown.DiscoveryError, context.DeadlineExceeded) || len(unknown.Models) != 0 {
		t.Fatal("unknown selection bypassed required discovery")
	}
	close(release)
	models := reg.EffectiveModels(t.Context(), "local")
	if len(models) != 0 || reg.ModelHasVision(t.Context(), "local", "known") {
		t.Fatal("successful empty discovery did not remove the old model")
	}
}

// Refresh with empty live discovery must keep the eligible catalog.
func TestRefreshDiscoveryKeepsEligibleCatalog(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfgDir)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	}))
	t.Cleanup(srv.Close)

	catalogPath := filepath.Join(t.TempDir(), "providers.yaml")
	testutil.FailErr(t, "write catalog", os.WriteFile(catalogPath, []byte(`
providers:
  - id: openai
    kind: openai
    label: OpenAI
    base_url: `+srv.URL+`/v1
    api_key_env: OPENAI_API_KEY
    models: []
    http_retry:
      max_retries: 1
      max_wait_ms: 1000
      backoff_ms: [1]
      statuses: [429]
      wait_headers: [Retry-After]
`), 0o600))

	localPath := filepath.Join(cfgDir, "providers.local.yaml")
	shipYAML, err := os.ReadFile(catalogPath)
	testutil.FailErr(t, "read ship", err)
	writeProvidersLocal(t, localPath, shipYAML)
	cat, err := NewProviderCatalog()
	testutil.FailErr(t, "catalog", err)
	creds, err := providercredentials.New()
	testutil.FailErr(t, "creds", err)
	testutil.FailErr(t, "set key", creds.Set("openai", "sk-test"))

	reg, err := NewRegistry(t.Context(), cat, creds)

	if err == nil {

		reg.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))

	}
	testutil.FailErr(t, "registry", err)
	reg.discoveryClient = srv.Client()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "modelfeed", "testdata", "models_dev_fixture.json"))
	testutil.FailErr(t, "fixture", err)
	feed, err := modelfeed.New(modelfeed.Options{
		CacheDir: filepath.Join(cfgDir, "modelfeed"),
		GetBytes: func(context.Context, string) ([]byte, error) { return raw, nil },
	})
	testutil.FailErr(t, "feed", err)
	_, err = feed.Refresh(context.Background())
	testutil.FailErr(t, "refresh feed", err)
	reg.SetModelFeed(t.Context(), feed)

	before := reg.EffectiveModels(t.Context(), "openai")
	if len(before) == 0 {
		t.Fatal("expected eligible catalog before refresh")
	}
	reg.RefreshDiscovery("openai")
	after := reg.EffectiveModels(t.Context(), "openai")
	if len(after) == 0 {
		t.Fatal("refresh must not wipe eligible catalog on empty discovery")
	}
	if len(after) != len(before) {
		t.Fatalf("eligible count changed after empty refresh: before=%d after=%d", len(before), len(after))
	}
	n, err := reg.TestConnectivity(t.Context(), "openai")
	testutil.FailErr(t, "TestConnectivity", err)
	if n != len(before) {
		t.Fatalf("TestConnectivity model count = %d, want %d (catalog kept)", n, len(before))
	}
}

type stubContextLength struct {
	n int
}

func (s stubContextLength) ContextLength(provider, model string) int {
	_ = provider
	_ = model
	return s.n
}

func testWindows(t *testing.T) modelinfo.ModelContextWindows {
	t.Helper()
	w, err := modelinfo.LoadModelContextWindows()
	testutil.FailErr(t, "LoadModelContextWindows failed", err)
	return w
}

func testBaseCompaction(t *testing.T) compaction.CompactionConfig {
	t.Helper()
	return compaction.DefaultCompactionConfig()
}

func TestResolveTrueWindowModelEntry(t *testing.T) {
	cfg := testBaseCompaction(t)
	windows := testWindows(t)
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"}}
	got, source := ResolveTrueWindow(policy, stubContextLength{n: 999999}, windows, cfg)
	if got != 999999 {
		t.Fatalf("true_window = %d want 999999 (ContextLength wins)", got)
	}
	if source != WindowResolveModelEntry {
		t.Fatalf("source = %q want %q", source, WindowResolveModelEntry)
	}
}

func TestResolveTrueWindowMatchTable(t *testing.T) {
	cfg := testBaseCompaction(t)
	windows := testWindows(t)
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"}}
	got, source := ResolveTrueWindow(policy, stubContextLength{n: 0}, windows, cfg)
	if got != 262144 {
		t.Fatalf("true_window = %d want 262144", got)
	}
	if source != WindowResolveMatchTable {
		t.Fatalf("source = %q want %q", source, WindowResolveMatchTable)
	}
}

func TestResolveTrueWindowFallback(t *testing.T) {
	cfg := testBaseCompaction(t)
	windows := testWindows(t)
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "x", Model: "totally-unknown-model"}}
	got, source := ResolveTrueWindow(policy, nil, windows, cfg)
	if got != 262144 {
		t.Fatalf("true_window = %d want fallback 262144", got)
	}
	if source != WindowResolveFallback {
		t.Fatalf("source = %q want %q", source, WindowResolveFallback)
	}
}

func TestApplyLiveBudgetKimiAuto(t *testing.T) {
	cfg := testBaseCompaction(t)
	windows := testWindows(t)
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"}}
	out, limits, err := ApplyLiveBudget(cfg, policy, nil, windows)
	testutil.FailErr(t, "ApplyLiveBudget failed", err)
	if out.ModelContextWindow != 196608 {
		t.Fatalf("live = %d want 196608", out.ModelContextWindow)
	}
	if limits.MaxToolResultBytes != 524288 || limits.MaxCoordinatorLoopCycles != 256 || limits.MaxWorkerSummaryChars != 16384 {
		t.Fatalf("limits = %+v", limits)
	}
	if out.MaxWorkerSummaryChars != 16384 {
		t.Fatalf("MaxWorkerSummaryChars = %d want 16384", out.MaxWorkerSummaryChars)
	}
}

func TestApplyLiveBudgetExplicit(t *testing.T) {
	cfg := testBaseCompaction(t)
	cfg.WindowSource = compaction.WindowSourceExplicit
	cfg.ModelContextWindow = 100000
	windows := testWindows(t)
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"}}
	out, _, err := ApplyLiveBudget(cfg, policy, stubContextLength{n: 262144}, windows)
	testutil.FailErr(t, "ApplyLiveBudget failed", err)
	if out.ModelContextWindow != 100000 {
		t.Fatalf("live = %d want 100000 (explicit)", out.ModelContextWindow)
	}
	if out.WindowSource != compaction.WindowSourceExplicit {
		t.Fatalf("WindowSource = %q", out.WindowSource)
	}
}

func TestLoadBundledModelContextWindows(t *testing.T) {
	w := testWindows(t)
	if w.FallbackTrueWindow != 262144 {
		t.Fatalf("fallback = %d", w.FallbackTrueWindow)
	}
	n, ok := w.Lookup("accounts/fireworks/models/kimi-k2p7-code")
	if !ok || n != 262144 {
		t.Fatalf("kimi lookup = %d ok=%v", n, ok)
	}
}
