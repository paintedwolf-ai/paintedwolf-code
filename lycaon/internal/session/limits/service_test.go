package limits

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/modelfeed"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFallbackBudgetKeepsOverridesAndDerivesWorkerLimit(t *testing.T) {
	cfg, derived := fallbackBudget(nil)
	if cfg.MaxWorkerSummaryChars <= 0 || cfg.MaxWorkerSummaryChars != derived.MaxWorkerSummaryChars {
		t.Fatalf("worker limit = %d, derived = %d", cfg.MaxWorkerSummaryChars, derived.MaxWorkerSummaryChars)
	}
	custom := compaction.DefaultCompactionConfig()
	custom.TargetTokens = 321
	custom.MaxWorkerSummaryChars = 123
	got, _ := fallbackBudget(fixedConfig{custom})
	if got.TargetTokens != 321 || got.MaxWorkerSummaryChars != 123 {
		t.Fatalf("fallback discarded explicit limits: %+v", got)
	}
}

type fixedConfig struct{ cfg compaction.CompactionConfig }

func (f fixedConfig) Config() compaction.CompactionConfig { return f.cfg }

type observedContextWindows map[llm.ModelRef]int

func (windows observedContextWindows) ContextLength(provider, model string) int {
	return windows[llm.ModelRef{ProviderID: provider, Model: model}]
}

func TestCompactionFollowsCurrentProviderModelFactsWithoutSessionCarryover(t *testing.T) {
	left := llm.ModelRef{ProviderID: "left", Model: "model"}
	right := llm.ModelRef{ProviderID: "right", Model: "model"}
	other := llm.ModelRef{ProviderID: "left", Model: "other"}
	facts := observedContextWindows{left: 16000, right: 64000, other: 32000}
	service := New(settings.DefaultSessionLimits(), nil)
	service.SetModelSources(nil, facts)
	session := &api.Session{ID: "same-session"}
	check := func(selection llm.ModelRef, window int) {
		t.Helper()
		session.ProviderID, session.Model = selection.ProviderID, selection.Model
		if actual := service.Compaction(t.Context(), session).ModelContextWindow; actual != window*75/100 {
			t.Fatalf("compaction retained another selection's facts: %+v window=%d", selection, actual)
		}
	}
	check(left, 16000)
	facts[left] = 8000
	check(left, 8000)
	check(right, 64000)
	check(other, 32000)
	// Replacing the provider's current facts also replaces its old endpoint's
	// observation; the session cannot retain a second authoritative limit.
	service.SetModelSources(nil, observedContextWindows{left: 48000})
	check(left, 48000)
}

func TestCompactionConfigFuncReapplies(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global.yaml")
	if err := os.WriteFile(global, []byte(`
coordinator:
  provider_id: openai
  model: gpt-4o
lite:
  provider_id: ""
  model: ""
agent_pool:
  selection: first
  models: []
`), 0o600); err != nil {
		testutil.FailErr(t, "write model policy fixture", err)
	}
	store, err := llm.NewPolicyStoreAt(global)
	testutil.FailErr(t, "NewPolicyStoreAt", err)

	mgr := New(settings.DefaultSessionLimits(), nil)
	mgr.SetModelSources(store, nil)
	first, _ := mgr.ResolveRoots(nil)
	if first.ModelContextWindow != 96000 { // 128000 * 75%
		t.Fatalf("gpt-4o live = %d want 96000", first.ModelContextWindow)
	}

	testutil.FailErr(t, "PutGlobal kimi", store.PutGlobal(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"},
	}))
	second, _ := mgr.ResolveRoots(nil)
	if second.ModelContextWindow != 196608 {
		t.Fatalf("kimi live = %d want 196608 (policy change without restart)", second.ModelContextWindow)
	}
	if first.ModelContextWindow == second.ModelContextWindow {
		t.Fatal("expected live budget to change after policy model change")
	}
}

func TestLiveBudgetResolveUsesProjectOverlay(t *testing.T) {
	tmp := t.TempDir()
	global := filepath.Join(tmp, "global.yaml")
	projectDir := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configtest.Overlay(t, map[config.Rel]string{config.ModelPolicy: `
coordinator:
  provider_id: openai
  model: gpt-4o
lite:
  provider_id: ""
  model: ""
agent_pool:
  selection: first
  models: []
`})
	store, err := llm.NewPolicyStoreAt(global)
	testutil.FailErr(t, "NewPolicyStoreAt", err)
	testutil.FailErr(t, "PutGlobal", store.PutGlobal(llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "openai", Model: "gpt-4o"},
	}))
	testutil.FailErr(t, "PutProject", store.PutProject(projectDir, llm.ModelPolicy{
		Coordinator: llm.ModelRef{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"},
	}))

	mgr := New(settings.DefaultSessionLimits(), nil)
	mgr.SetModelSources(store, nil)
	globalCfg, _ := mgr.ResolveRoots(nil)
	if globalCfg.ModelContextWindow != 96000 {
		t.Fatalf("global live = %d want 96000", globalCfg.ModelContextWindow)
	}
	ignored, _ := mgr.ResolveRoots(nil)
	if ignored.ModelContextWindow != 96000 {
		t.Fatalf("empty dir must stay global: %d", ignored.ModelContextWindow)
	}
	projectCfg, _ := mgr.ResolveRoots([]string{projectDir})
	if projectCfg.ModelContextWindow != 196608 {
		t.Fatalf("project live = %d want 196608", projectCfg.ModelContextWindow)
	}
	if got := mgr.Compaction(t.Context(), nil); got.ModelContextWindow != 96000 {
		t.Fatalf("nil session compaction window = %d want 96000", got.ModelContextWindow)
	}
	if got := mgr.Compaction(t.Context(), &api.Session{ProviderID: "fw", Model: "accounts/fireworks/models/kimi-k2p7-code"}); got.ModelContextWindow != 196608 {
		t.Fatalf("session model compaction window = %d want 196608", got.ModelContextWindow)
	}
}

type coordinatorPolicy struct{ ref llm.ModelRef }

func (p coordinatorPolicy) GetForProjectRoots([]string) (llm.ModelPolicy, error) {
	return llm.ModelPolicy{Coordinator: p.ref}, nil
}

// feedOnlyLimits wires a real registry whose discovery returns ids without lengths.
func feedOnlyLimits(t *testing.T, model string, feedWindow int) (*Service, *api.Session, *api.Session) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": model}, {"id": "not-in-feed"}}})
	}))
	t.Cleanup(server.Close)
	shipYAML := "providers:\n  - id: hosted\n    kind: openai\n    base_url: " + server.URL + "/v1\n    api_key_env: \"\"\n    models: []\n    http_retry:\n      max_retries: 1\n      max_wait_ms: 1000\n      backoff_ms: [1]\n      statuses: [429]\n      wait_headers: [Retry-After]\n"
	configtest.Overlay(t, map[config.Rel]string{config.Providers: shipYAML})
	localPath := filepath.Join(t.TempDir(), "providers.local.yaml")
	testutil.FailErr(t, "write providers", os.WriteFile(localPath, []byte(shipYAML), 0o600))
	catalog, err := llm.NewProviderCatalogAt(localPath)
	testutil.FailErr(t, "catalog", err)
	creds := providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age"))
	testutil.FailErr(t, "set key", creds.Set("hosted", "test-key"))
	reg, err := llm.NewRegistry(t.Context(), catalog, creds)
	testutil.FailErr(t, "registry", err)
	raw, err := json.Marshal(map[string]any{"openai": map[string]any{"id": "openai", "models": map[string]any{model: map[string]any{
		"id": model, "tool_call": true, "modalities": map[string]any{"output": []string{"text"}}, "limit": map[string]int{"context": feedWindow},
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
	mgr := New(settings.DefaultSessionLimits(), nil)
	mgr.SetModelSources(coordinatorPolicy{llm.ModelRef{ProviderID: "hosted", Model: model}}, reg)
	return mgr, &api.Session{ProviderID: "hosted", Model: model}, &api.Session{ProviderID: "hosted", Model: "not-in-feed"}
}

func TestFeedOnlyWindowBoundsLimits(t *testing.T) {
	t.Run("a 32K window lowers every budget", func(t *testing.T) {
		mgr, sess, unknown := feedOnlyLimits(t, "feed-small", 32768)
		got, fallback := mgr.Compaction(t.Context(), sess), mgr.Compaction(t.Context(), unknown)
		if fallback.ModelContextWindow != 196608 {
			t.Fatalf("fallback live budget = %d", fallback.ModelContextWindow)
		}
		if got.ModelContextWindow != 24576 {
			t.Fatalf("live budget = %d, want 24576", got.ModelContextWindow)
		}
		for _, budget := range []struct {
			name           string
			value, ceiling int
		}{
			{"live", got.ModelContextWindow, fallback.ModelContextWindow},
			{"hard ceiling", got.HardCeilingTokens, fallback.HardCeilingTokens},
			{"target", got.TargetTokens, fallback.TargetTokens},
			{"trigger", got.CompactionTriggerTokens(), fallback.CompactionTriggerTokens()},
		} {
			if budget.value <= 0 || budget.value >= budget.ceiling {
				t.Errorf("%s = %d, want below the fallback's %d", budget.name, budget.value, budget.ceiling)
			}
		}
		cfg, derived := mgr.ResolveRoots(nil)
		if cfg.ModelContextWindow != 24576 || derived.MaxToolResultBytes != 65536 || derived.MaxCoordinatorLoopCycles != 32 || derived.MaxWorkerSummaryChars != 2048 {
			t.Fatalf("resolved live %d, derived %+v", cfg.ModelContextWindow, derived)
		}
	})
	t.Run("a 1M window stays at the absolute cap", func(t *testing.T) {
		mgr, sess, _ := feedOnlyLimits(t, "feed-large", 1_048_572)
		got := mgr.Compaction(t.Context(), sess)
		if got.ModelContextWindow != 196608 || got.HardCeilingTokens != 196608*85/100 {
			t.Fatalf("live %d, hard %d", got.ModelContextWindow, got.HardCeilingTokens)
		}
	})
}
