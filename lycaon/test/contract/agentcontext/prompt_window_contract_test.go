package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestCompactionBudgetWithinModelWindow keeps live budgets within the true window.
func TestCompactionBudgetWithinModelWindow(t *testing.T) {
	t.Parallel()

	budgets, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)
	am := budgets.AbsoluteMaximums
	if am == nil || am.ModelContextWindowTokens <= 0 {
		t.Fatal("prompt-budgets.yaml: absolute_maximums.model_context_window_tokens required as the true-window SSOT")
	}
	if strings.TrimSpace(am.Model) == "" {
		t.Fatal("prompt-budgets.yaml: absolute_maximums.model required")
	}

	windows, err := modelinfo.LoadModelContextWindows()
	contractcheck.FailErr(t, "LoadModelContextWindows", err)
	cfg := compaction.DefaultCompactionConfig()

	policy := llm.ModelPolicy{Coordinator: llm.ModelRef{Model: am.Model}}
	trueWindow, source := llm.ResolveTrueWindow(policy, nil, windows, cfg)
	if trueWindow != am.ModelContextWindowTokens {
		t.Errorf("absolute_maximums.model_context_window_tokens (%d) != ResolveTrueWindow(%q)=%d (source=%s)",
			am.ModelContextWindowTokens, am.Model, trueWindow, source)
	}

	live, _, err := llm.ApplyLiveBudget(cfg, policy, nil, windows)
	contractcheck.FailErr(t, "ApplyLiveBudget", err)
	if live.ModelContextWindow > trueWindow {
		t.Errorf("live_budget (%d) exceeds true_window (%d)", live.ModelContextWindow, trueWindow)
	}
	if live.HardCeilingTokens > trueWindow {
		t.Errorf("compaction hard ceiling (%d) exceeds true_window (%d)", live.HardCeilingTokens, trueWindow)
	}
}
