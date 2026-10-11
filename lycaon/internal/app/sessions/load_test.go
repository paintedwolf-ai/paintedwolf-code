package sessions

import (
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSessionCompactorUsesConfiguredPhysicalWindowWithoutProviderDiscovery(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.ModelContextWindow: "fallback_true_window: 8192\nwindows:\n  - match: [fixture-window]\n    context_window: 16384\n"})
	// The empty registry has no provider transport. This matrix only selects
	// summarizer construction; BudgetRemaining reads its window without inference.
	for _, mock := range []string{"1", "0"} {
		t.Run(mock, func(t *testing.T) {
			t.Setenv("LYCAON_LLM_MOCK", mock)
			svc := &llm.Service{Registry: &llm.Registry{}, Policy: llm.NewInMemoryPolicyStore(llm.ModelPolicy{Coordinator: llm.ModelRef{ProviderID: "unconfigured", Model: "fixture-window"}})}
			compactor, err := loadCompactor(svc, "", nil)
			testutil.FailErr(t, "load session compactor", err)
			cfg := compactor.Config()
			if cfg.ModelContextWindow != 12288 || cfg.TargetTokens <= 0 || cfg.TargetTokens >= cfg.ModelContextWindow {
				t.Fatalf("session window was not derived from configured physical capacity:%+v", cfg)
			}
			remaining, err := compactor.BudgetRemaining(t.Context(), "session", cfg.TargetTokens)
			testutil.FailErr(t, "read exhausted compaction budget", err)
			if remaining != 0 {
				t.Fatalf("target budget remaining=%d", remaining)
			}
		})
	}
	fallback, err := loadCompactor(nil, "", nil)
	testutil.FailErr(t, "load fallback compactor", err)
	if fallback.Config().ModelContextWindow != 6144 {
		t.Fatalf("fallback window=%d", fallback.Config().ModelContextWindow)
	}
}

func TestSessionCompactorRefusesInvalidWindowCatalog(t *testing.T) {
	configtest.Overlay(t, map[config.Rel]string{config.ModelContextWindow: "windows: ["})
	if compactor, err := loadCompactor(nil, "", nil); err == nil || compactor != nil {
		t.Fatalf("invalid model window catalog accepted:%v,%v", compactor, err)
	}
}
