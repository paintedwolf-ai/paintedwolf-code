package search

import "testing"

func TestParseSearchBudget(t *testing.T) {
	cases := []struct {
		raw  string
		want SearchBudget
		ok   bool
	}{
		{"", BudgetComplete, true},
		{"complete", BudgetComplete, true},
		{"INTERACTIVE", BudgetInteractive, true},
		{"interactive", BudgetInteractive, true},
		{"fast", "", false},
	}
	for _, tc := range cases {
		got, ok := ParseSearchBudget(tc.raw)
		if ok != tc.ok || got != tc.want {
			t.Fatalf("ParseSearchBudget(%q) = %q, %v; want %q, %v", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSearchBudgetCaps(t *testing.T) {
	if lines, files := BudgetComplete.codeCaps(); lines != SearchExecutorProbeHits || files != SearchExecutorProbeHits {
		t.Fatalf("complete caps = %d/%d", lines, files)
	}
	if lines, files := BudgetInteractive.codeCaps(); lines != InteractiveCodeMaxHits+1 || files != InteractiveFileMaxHits+1 {
		t.Fatalf("interactive caps = %d/%d", lines, files)
	}
	if BudgetInteractive.storeCap() != InteractiveStoreMaxHits+1 {
		t.Fatalf("interactive store cap = %d", BudgetInteractive.storeCap())
	}
	lines, files := SearchBudget("").codeCaps()
	if lines != SearchExecutorProbeHits || files != SearchExecutorProbeHits {
		t.Fatalf("zero budget caps = %d/%d, want complete", lines, files)
	}
}

func TestOrderCodeRootsOriginFirst(t *testing.T) {
	roots := []CodeRoot{
		{ProjectID: "other", Path: "/other"},
		{ProjectID: "origin", Path: "/origin-a"},
		{ProjectID: "origin", Path: "/origin-b"},
	}
	got := orderCodeRoots(roots, "origin")
	if len(got) != 3 || got[0].Path != "/origin-a" || got[1].Path != "/origin-b" || got[2].Path != "/other" {
		t.Fatalf("order = %+v", got)
	}
	if got := orderCodeRoots(roots, ""); got[0].Path != "/other" {
		t.Fatalf("empty origin must keep input order: %+v", got)
	}
}
