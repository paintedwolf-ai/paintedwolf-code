package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/prompts"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestShippedPromptSizePolicyIsReviewable(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(promptBudgetPolicyFile(contractcheck.RepoRoot(t)))
	contractcheck.FailErr(t, "read prompt budgets", err)
	policy, err := decodePromptSizePolicy(raw)
	contractcheck.FailErr(t, "decode prompt size policy", err)
	budgets, err := prompts.LoadPromptBudgets()
	contractcheck.FailErr(t, "LoadPromptBudgets", err)
	for _, name := range promptBudgetSuite.CategoryNames() {
		if policy.Limits[name].Limit != budgets.Sizes.Limits[name].Limit {
			t.Errorf("%s limit: contract reads %d, runtime reads %d", name, policy.Limits[name].Limit, budgets.Sizes.Limits[name].Limit)
		}
	}
}

func TestPromptBudgetCatalogMatchesRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	reg, err := LoadPromptBudgetRegistry(lycaonRoot)
	contractcheck.FailErr(t, "LoadPromptBudgetRegistry", err)
	catalog := buildPromptBudgetCatalog(reg)

	assertCatalogHas := func(category string, ids ...string) {
		t.Helper()
		entries, ok := catalog[category]
		if !ok {
			t.Fatalf("buildPromptBudgetCatalog missing category %q", category)
		}
		for _, id := range ids {
			if _, ok := entries[id]; !ok {
				t.Fatalf("buildPromptBudgetCatalog missing %s/%s", category, id)
			}
		}
	}

	assertCatalogHas("worker_personas", reg.WorkerPersonaIDs()...)
	for _, row := range reg.Tripartite {
		assertCatalogHas("coordinator_tripartite", row.Name)
	}
	for _, spec := range reg.Injects {
		assertCatalogHas("coordinator_injects", spec.ID)
	}
	for id := range reg.AgentTemplates {
		assertCatalogHas("agent_templates", id)
	}
	assertCatalogHas("kicks", reg.KickIDs...)
}

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
