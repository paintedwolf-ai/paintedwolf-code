package compaction

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// recoveryRow builds a host draft-recovery guidance row preceding the draft it targets.
func recoveryRow(id, target string) ContextMessage {
	return ContextMessage{
		ID:      id,
		Role:    string(api.MessageRoleUser),
		Content: "Reissue the interrupted draft with its recorded arguments.",
		ContextMessageProvenance: ContextMessageProvenance{
			Origin:              api.MessageOriginHost,
			Authority:           api.ContentAuthoritySystem,
			Visibility:          api.MessageVisibilityInternal,
			DraftRecoveryTarget: target,
		},
	}
}

func draftRow(id string) ContextMessage {
	return ContextMessage{ID: id, Role: string(api.MessageRoleAssistant), Content: "draft reply"}
}

func TestRecentCompactionStartKeepsRecoveryRowsWithTheirDraft(t *testing.T) {
	rows := []ContextMessage{
		{ID: "old", Role: string(api.MessageRoleUser), Content: "earlier request"},
		recoveryRow("rec-1", "draft"),
		recoveryRow("rec-2", "draft"),
		draftRow("draft"),
		{ID: "result", Role: string(api.MessageRoleTool), ToolCallID: "t", Content: "result"},
	}
	if start := recentCompactionStart(rows, 2); start != 1 {
		t.Fatalf("recent window started at %d, want the recovery rows retained ahead of their draft", start)
	}
	plain := []ContextMessage{
		{ID: "old", Role: string(api.MessageRoleUser), Content: "earlier request"},
		draftRow("draft"),
		{ID: "result", Role: string(api.MessageRoleTool), ToolCallID: "t", Content: "result"},
	}
	if start := recentCompactionStart(plain, 2); start != 1 {
		t.Fatalf("window without recovery rows started at %d, want the draft row", start)
	}
	gapped := []ContextMessage{
		recoveryRow("rec-other", "other"),
		draftRow("draft"),
		{ID: "result", Role: string(api.MessageRoleTool), ToolCallID: "t", Content: "result"},
	}
	if start := recentCompactionStart(gapped, 2); start != 1 {
		t.Fatalf("window with an unrelated recovery row started at %d, want the walk to stop at the mismatch", start)
	}
}

func TestCompleteFitStartAdvancesPastDraftRecoveryOrphans(t *testing.T) {
	t.Run("recovery row separated from its draft", func(t *testing.T) {
		body := []ContextMessage{
			draftRow("draft"),
			{ID: "mid", Role: string(api.MessageRoleAssistant), Content: "later turn"},
			recoveryRow("rec", "draft"),
			{ID: "tail", Role: string(api.MessageRoleAssistant), Content: "newest turn"},
		}
		if start := completeFitStart(body, 1); start != 3 {
			t.Fatalf("cut start = %d, want it advanced past the recovery row whose draft was cut", start)
		}
	})
	t.Run("draft separated from its recovery row", func(t *testing.T) {
		body := []ContextMessage{
			recoveryRow("rec", "draft"),
			{ID: "mid", Role: string(api.MessageRoleAssistant), Content: "later turn"},
			draftRow("draft"),
			{ID: "tail", Role: string(api.MessageRoleAssistant), Content: "newest turn"},
		}
		if start := completeFitStart(body, 1); start != 3 {
			t.Fatalf("cut start = %d, want it advanced past the draft whose recovery row was cut", start)
		}
	})
	t.Run("paired rows after the cut stay", func(t *testing.T) {
		body := []ContextMessage{
			{ID: "mid", Role: string(api.MessageRoleAssistant), Content: "later turn"},
			draftRow("draft"),
			recoveryRow("rec", "draft"),
		}
		if start := completeFitStart(body, 1); start != 1 {
			t.Fatalf("cut start = %d, want the paired rows kept together after the cut", start)
		}
	})
}

func TestReductionSourceBudgetsKeepRecoveryGroupsWithTheirDraft(t *testing.T) {
	rows := []ContextMessage{
		{ID: "old", Role: string(api.MessageRoleUser), Content: "earlier request"},
		recoveryRow("rec-1", "draft"),
		recoveryRow("rec-2", "draft"),
		draftRow("draft"),
		{ID: "tail", Role: string(api.MessageRoleAssistant), Content: "newest turn"},
	}
	if got := boundedReductionPrefix(rows, 100, 100000, 1000000); len(got) != len(rows) {
		t.Fatalf("generous budget kept %d of %d rows", len(got), len(rows))
	}
	if got := boundedReductionPrefix(rows, 4, 100000, 1000000); len(got) != 4 {
		t.Fatalf("row budget kept %d rows, want the recovery group retained with its draft", len(got))
	}
	if got := boundedReductionPrefix(rows, 3, 100000, 1000000); len(got) != 1 {
		t.Fatalf("row budget kept %d rows, want the recovery group excluded whole instead of split", len(got))
	}
}

func TestBeforeWorkingSetUsesDraftOrdinalForRecoveryRows(t *testing.T) {
	draft := draftRow("draft")
	draft.Ord = 5
	rec := recoveryRow("rec", "draft")
	rec.Ord = 12
	unmatched := recoveryRow("unmatched", "missing")
	unmatched.Ord = 13
	lateTarget := recoveryRow("late", "newdraft")
	lateTarget.Ord = 3
	newdraft := draftRow("newdraft")
	newdraft.Ord = 20
	tail := draftRow("tail")
	tail.Ord = 15
	rows := []ContextMessage{draft, rec, unmatched, lateTarget, newdraft, tail}
	var ids []string
	for _, row := range beforeWorkingSet(rows, 10) {
		ids = append(ids, row.ID)
	}
	if !slices.Equal(ids, []string{"draft", "rec", "late"}) {
		t.Fatalf("working set = %v, want the draft, its recovery row on the draft ordinal, and the early recovery row", ids)
	}
}

func TestOldestCompactionPrefixKeepsRecoveryGroupsWithTheirDraft(t *testing.T) {
	rows := []ContextMessage{
		draftRow("a0"), draftRow("a1"), draftRow("a2"),
		recoveryRow("rec-1", "draft"), recoveryRow("rec-2", "draft"),
		draftRow("draft"),
		{ID: "t", Role: string(api.MessageRoleTool), ToolCallID: "x", Content: "result"},
	}
	if got := oldestCompactionPrefix(rows, 4); len(got) != len(rows) {
		t.Fatalf("prefix kept %d of %d rows, want the recovery group, its draft, and the trailing result", len(got), len(rows))
	}
	truncated := []ContextMessage{
		draftRow("a0"), draftRow("a1"), draftRow("a2"),
		recoveryRow("rec-1", "draft"), recoveryRow("rec-2", "draft"),
	}
	if got := oldestCompactionPrefix(truncated, 4); len(got) != len(truncated) {
		t.Fatalf("prefix kept %d of %d rows, want the trailing recovery group retained when its draft is absent", len(got), len(truncated))
	}
	plain := []ContextMessage{draftRow("a0"), draftRow("a1"), draftRow("a2"), draftRow("a3"), draftRow("a4")}
	if got := oldestCompactionPrefix(plain, 4); len(got) != 4 {
		t.Fatalf("prefix kept %d rows without recovery rows, want the limit unchanged", len(got))
	}
}
