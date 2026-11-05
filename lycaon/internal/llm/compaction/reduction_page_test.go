package compaction

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReductionPageAcceptanceIgnoresUntouchedSuffix(t *testing.T) {
	c := savingsCompactor(t, nil)
	c.cfg.MessageSlice = 4
	var eligible []ContextMessage
	for i := range 8 {
		eligible = append(eligible, ContextMessage{ID: fmt.Sprint(i), Role: "assistant", Content: strings.Repeat("recorded evidence ", 100)})
	}
	page, err := c.reductionPage(t.Context(), SessionInfo{}, eligible, eligible)
	testutil.FailErr(t, "select page", err)
	for _, count := range []int{1, 20, 100} {
		all := append([]ContextMessage(nil), eligible...)
		for i := range count {
			all = append(all, ContextMessage{ID: fmt.Sprintf("suffix-%d", i), Role: "assistant", Content: strings.Repeat("untouched recent result ", 500)})
		}
		candidate := append(append([]ContextMessage(nil), page.replacement...), remainingCompactionRows(all, retainedMessageIDs(page.source))...)
		useful, err := c.usefulReduction(SessionInfo{}, page, all, candidate)
		testutil.FailErr(t, "measure page", err)
		if !useful {
			t.Fatalf("untouched suffix of %d messages prevented a useful page reduction", count)
		}
	}
}

func TestReductionPageExpandsSmallExchangesWithinItsWorkBudget(t *testing.T) {
	c := savingsCompactor(t, nil)
	c.cfg.MessageSlice = 2
	var rows []ContextMessage
	for i := range 50 {
		rows = append(rows, ContextMessage{ID: fmt.Sprint(i), Role: "assistant", Content: strings.Repeat("value ", 80)})
	}
	page, err := c.reductionPage(t.Context(), SessionInfo{}, rows, rows)
	testutil.FailErr(t, "select expanded page", err)
	if len(page.source) <= c.cfg.MessageSlice || len(page.source) > c.cfg.MessageSlice*4 {
		t.Fatalf("selected %d rows outside adaptive bounds", len(page.source))
	}
}

func TestReductionSourceBudgetsNeverSplitToolExchanges(t *testing.T) {
	rows := []ContextMessage{
		{ID: "intro", Role: "user", Content: "Inspect the sources."},
		{ID: "call", Role: "assistant", ToolCalls: []api.ToolCall{{ID: "a", Name: "read"}, {ID: "b", Name: "read"}}},
		{ID: "a", Role: "tool", ToolCallID: "a", Content: "first result"},
		{ID: "b", Role: "tool", ToolCallID: "b", Content: "second result"},
	}
	for _, bounds := range [][3]int{{3, 10000, 10000}, {10, 10, 10000}, {10, 10000, 300}} {
		selected := boundedReductionPrefix(rows, bounds[0], bounds[1], bounds[2])
		if len(selected) > 1 && len(selected) < len(rows) {
			t.Fatalf("budget %v split a producing call from its results", bounds)
		}
	}
}

func TestReductionRejectsWholeHistoryGrowth(t *testing.T) {
	c := savingsCompactor(t, nil)
	all := []ContextMessage{{ID: "old", Role: "assistant", Content: strings.Repeat("old evidence ", 500)}}
	page, err := c.reductionPage(t.Context(), SessionInfo{}, all, all)
	testutil.FailErr(t, "select page", err)
	candidate := append(append([]ContextMessage(nil), page.replacement...), ContextMessage{Role: "assistant", Content: strings.Repeat("accidental duplication ", 1000)})
	accepted, err := c.usefulReduction(SessionInfo{}, page, all, candidate)
	testutil.FailErr(t, "reject growth", err)
	if accepted {
		t.Fatal("page savings admitted whole-history growth")
	}
}
