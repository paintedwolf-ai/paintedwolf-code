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
