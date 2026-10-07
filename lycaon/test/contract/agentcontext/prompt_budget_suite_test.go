package contract

import (
	"os"
	"strings"
	"testing"

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

func TestTightenPromptBudgetFileKeepsAuthoredForm(t *testing.T) {
	t.Parallel()
	const raw = `# Header comment.
version: 1
sizes:
    # Limits comment.
    limits:
        kicks: {warn: 1, limit: 2}
    # Grandfathered comment.
    grandfathered:
        kicks:
            big: 9
            gone: 7
    exceptions: {}
perception:
    max_tool_images: 8
`
	out, err := tightenPromptBudgetFile([]byte(raw), map[string]map[string]int{"kicks": {"big": 5}})
	contractcheck.FailErr(t, "tighten", err)
	text := string(out)
	for _, want := range []string{"# Header comment.", "# Limits comment.", "# Grandfathered comment.",
		"kicks: {warn: 1, limit: 2}", "big: 5", "max_tool_images: 8"} {
		if !strings.Contains(text, want) {
			t.Errorf("tightened file lost %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "gone") || strings.Contains(text, "big: 9") {
		t.Errorf("tightened file kept a dropped or loosened cap:\n%s", text)
	}
	if _, err := tightenPromptBudgetFile([]byte("version: 1\n"), nil); err == nil {
		t.Fatal("tightened a file without sizes")
	}
}
