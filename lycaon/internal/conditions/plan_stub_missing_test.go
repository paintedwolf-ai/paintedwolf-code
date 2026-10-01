package conditions_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
)

func TestPlanStubMissingRequiredWrongHeadings(t *testing.T) {
	body := "---\ntitle: x\n---\n\n# Plan\n\n## Scope\n\nBuild a CLI.\n\n## API design\n\nUse Open-Meteo.\n"
	missing := conditions.PlanStubMissingRequired(body)
	joined := strings.Join(missing, ",")
	for _, want := range []string{"## Goal", "## Assumptions", "research_depth", "## Approach"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing=%v want %q", missing, want)
		}
	}
	// ## Scope aliases ## Plan implementation scope — should not be listed as missing.
	for _, m := range missing {
		if m == "## Plan implementation scope" {
			t.Fatalf("## Scope should satisfy implementation scope; missing=%v", missing)
		}
	}
	present := conditions.MarkdownH2Headings(body)
	if len(present) < 2 {
		t.Fatalf("present=%v", present)
	}
}

func TestPlanStubMissingRequiredComplete(t *testing.T) {
	body := conditions.TestPlanContentStubOnly
	if got := conditions.PlanStubMissingRequired(body); len(got) != 0 {
		t.Fatalf("complete stub missing=%v", got)
	}
}
