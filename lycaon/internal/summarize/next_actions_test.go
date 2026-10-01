package summarize

import (
	"context"
	"strings"
	"testing"
)

func TestFinalizeNextActionsCapsAndPrefersSummarize(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.NextActionsMax = 4
	pack := ContextPack{
		Skeleton: []PackSymbol{
			{Path: "pkg/a", Kind: KindDirectoryRollup, Name: "10 files"},
			{Path: "pkg/b", Kind: KindDirectoryRollup, Name: "8 files"},
			{Path: "pkg/c", Kind: KindDirectoryRollup, Name: "5 files"},
		},
		Gaps: []string{"pkg/a/x.go:10", "pkg/b/y.go:20", "pkg/c/z.go:30", "pkg/d/w.go:40"},
	}
	actions := []NextAction{
		{Tool: "read", Path: "pkg/a/x.go", Lines: "1-24", Why: "ranked leftover"},
		{Tool: "read", Path: "pkg/b/y.go", Lines: "1-24", Why: "ranked leftover"},
		{Tool: "read", Path: "pkg/extra.go", Lines: "1-24", Why: "ranked leftover"},
		{Tool: "read", Path: "pkg/more.go", Lines: "1-24", Why: "ranked leftover"},
		{Tool: "read", Path: "pkg/even.go", Lines: "1-24", Why: "ranked leftover"},
		{Tool: "summarize", Path: "pkg/abort", Why: "rolled-up — summarize for depth"},
	}
	got := finalizeNextActions(context.Background(), noRerank, "how does abort work?", actions, pack, caps)
	if len(got) > caps.Pack.NextActionsMax {
		t.Fatalf("len=%d exceeds next_actions_max=%d: %#v", len(got), caps.Pack.NextActionsMax, got)
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty shortlist")
	}
	// Task path affinity + summarize preference should surface abort first.
	if got[0].Tool != "summarize" || !strings.Contains(got[0].Path, "abort") {
		t.Fatalf("first=%#v, want summarize …/abort", got[0])
	}
	summarizeN := 0
	for _, a := range got {
		if a.Tool == "summarize" {
			summarizeN++
		}
	}
	if summarizeN < 1 {
		t.Fatalf("expected summarize zooms in shortlist: %#v", got)
	}
}

func TestFinalizeNextActionsPrefersRelevantGapOverUnrelatedSummary(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.NextActionsMax = 2
	actions := []NextAction{
		{Tool: "summarize", Path: "pkg/rendering", Why: "rolled-up — summarize for depth"},
		{Tool: "read", Path: "pkg/auth/token.go", Lines: "40-80", Why: "inspect authentication token validation gap"},
	}

	got := finalizeNextActions(context.Background(), noRerank, "how is the authentication token validated?", actions, ContextPack{}, caps)
	if len(got) != 2 || got[0].Tool != "read" || got[0].Path != "pkg/auth/token.go" {
		t.Fatalf("got %#v, want the task-relevant read first", got)
	}
}

func TestFinalizeNextActionsUsesBreadthAsTieBreakWithoutTaskSignal(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.NextActionsMax = 2
	actions := []NextAction{
		{Tool: "read", Path: "pkg/a.go"},
		{Tool: "summarize", Path: "pkg/b"},
	}

	got := finalizeNextActions(context.Background(), noRerank, "", actions, ContextPack{}, caps)
	if len(got) != 2 || got[0].Tool != "summarize" {
		t.Fatalf("got %#v, want summarize as the no-signal tie-break", got)
	}
}

func TestClampNextActionsPreservesPrefix(t *testing.T) {
	caps := DefaultCaps()
	caps.Pack.NextActionsMax = 2
	in := []NextAction{
		{Tool: "grep", Path: "pkg", Pattern: "abort", Why: "full hits"},
		{Tool: "read", Path: "a.go", Why: "a"},
		{Tool: "read", Path: "b.go", Why: "b"},
	}
	got := clampNextActions(in, caps)
	if len(got) != 2 || got[0].Tool != "grep" {
		t.Fatalf("got %#v, want grep kept first under clamp", got)
	}
}
