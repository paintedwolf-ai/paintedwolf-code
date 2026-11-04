package llm

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

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

func TestLiveBudgetIsCappedAbsolutely(t *testing.T) {
	policy := ModelPolicy{Coordinator: ModelRef{ProviderID: "fixture", Model: "unlisted-model"}}
	for _, tc := range []struct {
		name         string
		maxLive      int
		window, live int
	}{
		{name: "larger window never raises it", window: 1_048_572, live: 196608},
		{name: "fallback sized window meets it", window: 262144, live: 196608},
		{name: "smaller window lowers it", window: 32768, live: 24576},
		{name: "project raises it deliberately", maxLive: 400000, window: 1_048_572, live: 400000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := compaction.DefaultCompactionConfig()
			if tc.maxLive > 0 {
				cfg.MaxLiveBudgetTokens = tc.maxLive
			}
			out, _, err := ApplyLiveBudget(cfg, policy, stubContextLength{n: tc.window}, testWindows(t))
			testutil.FailErr(t, "ApplyLiveBudget", err)
			if out.ModelContextWindow != tc.live || out.HardCeilingTokens != tc.live*85/100 {
				t.Fatalf("live %d hard %d, want live %d", out.ModelContextWindow, out.HardCeilingTokens, tc.live)
			}
		})
	}
	cfg := compaction.DefaultCompactionConfig()
	cfg.MaxLiveBudgetTokens = 0
	if out, _, err := ApplyLiveBudget(cfg, policy, stubContextLength{n: 1_048_572}, testWindows(t)); err != nil || out.ModelContextWindow != compaction.DefaultMaxLiveBudgetTokens {
		t.Fatalf("omitted cap: live %d err %v", out.ModelContextWindow, err)
	}
	if _, err := compaction.NormalizeConfig(compaction.CompactionConfig{ModelContextWindow: 1, MaxLiveBudgetTokens: -1}); err == nil {
		t.Fatal("negative cap accepted")
	}
}
