package workflow

import (
	"testing"
)

func TestHostStringSliceVarReadsSliceAny(t *testing.T) {
	vars := map[string]any{"last_failed_leaves": []any{"human_approval", "research_satisfied"}}
	got := hostStringSliceVar(vars, "last_failed_leaves")
	if len(got) != 2 || got[0] != "human_approval" {
		t.Fatalf("hostStringSliceVar = %v", got)
	}
}

func TestLatestEffectiveSummaryEmptyRecords(t *testing.T) {
	if _, ok := LatestEffectiveSummary(nil); ok {
		t.Fatal("expected false for nil records")
	}
}
