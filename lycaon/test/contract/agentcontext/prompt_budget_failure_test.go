package contract

import (
	"strings"
	"testing"
)

func TestFormatPromptBudgetFailureReportOverCap(t *testing.T) {
	t.Parallel()
	catalog := map[string]map[string]promptBudgetEntry{
		"coordinator_tripartite": {
			"implement_investigate_first_user": {
				Measures:  "Tripartite compile fixture",
				Fixture:   "surface=implement_investigate | visible user first turn",
				TrimPaths: []string{"lycaon/config/packs/painted-wolf/platform/shared/partials/coordinator-worker-chain-baseline.md"},
			},
		},
	}
	report := formatPromptBudgetFailureReport([]promptBudgetViolation{{
		Category: "coordinator_tripartite",
		ID:       "implement_investigate_first_user",
		Kind:     promptBudgetOverCap,
		Measured: 22000,
		Cap:      21489,
	}}, catalog)
	for _, want := range []string{
		"prompt budget: 1 violation",
		"coordinator_tripartite / implement_investigate_first_user",
		"over by: 511 bytes",
		"trim (edit these first):",
		"coordinator-worker-chain-baseline.md",
		"refresh all caps:",
		"UPDATE_PROMPT_BUDGETS=1",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}
