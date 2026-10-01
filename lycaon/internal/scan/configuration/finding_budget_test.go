package configuration_test

import (
	"testing"

	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scanfindings "github.com/lycaon/lycaon/internal/scan/findings"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFindingBudgetDedupeByRuleID(t *testing.T) {
	budget := scancfg.NewFindingBudget(scancfg.AgentBudgetConfig{
		MaxGuidanceFindings: 10,
		MinSeverity:         "info",
		DedupeBy:            "rule_id",
	})
	in := []api.SecurityFinding{
		scanfindings.FixtureFinding("r1", api.FindingLevelHigh, "", "a.go", 0),
		scanfindings.FixtureFinding("r1", api.FindingLevelHigh, "", "b.go", 0),
		scanfindings.FixtureFinding("r2", api.FindingLevelMedium, "", "c.go", 0),
	}
	out := budget.Apply(in, nil)
	if len(out.Findings) != 2 {
		t.Fatalf("findings = %d", len(out.Findings))
	}
}

func TestFindingBudgetRankAndCap(t *testing.T) {
	budget := scancfg.NewFindingBudget(scancfg.AgentBudgetConfig{
		MaxGuidanceFindings: 2,
		MinSeverity:         "info",
		Prioritize:          []string{"error", "warning", "info"},
	})
	in := []api.SecurityFinding{
		scanfindings.FixtureFinding("i1", api.FindingLevelInfo, "", "", 0),
		scanfindings.FixtureFinding("w1", api.FindingLevelMedium, "", "", 0),
		scanfindings.FixtureFinding("e1", api.FindingLevelHigh, "", "", 0),
		scanfindings.FixtureFinding("e2", api.FindingLevelHigh, "", "", 0),
	}
	out := budget.Apply(in, nil)
	if len(out.Findings) != 2 || !out.Truncated {
		t.Fatalf("findings = %#v truncated=%v", out.Findings, out.Truncated)
	}
	if out.Findings[0].RuleID != "e1" || out.Findings[1].RuleID != "e2" {
		t.Fatalf("order = %#v", out.Findings)
	}
}

func TestFindingBudgetPreferTouchedPaths(t *testing.T) {
	budget := scancfg.NewFindingBudget(scancfg.AgentBudgetConfig{
		MaxGuidanceFindings: 10,
		MinSeverity:         "warning",
		Prioritize:          []string{"error", "warning"},
		PreferPaths:         "touched_by_leg",
	})
	in := []api.SecurityFinding{
		scanfindings.FixtureFinding("w-other", api.FindingLevelMedium, "", "pkg/other.go", 0),
		scanfindings.FixtureFinding("w-touch", api.FindingLevelMedium, "", "internal/touched.go", 0),
	}
	out := budget.Apply(in, []string{"internal/touched.go"})
	if len(out.Findings) != 2 || out.Findings[0].RuleID != "w-touch" {
		t.Fatalf("order = %#v", out.Findings)
	}
}
