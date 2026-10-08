package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAppendUserNudgeDefersStoreWhileDraftSlotOpen(t *testing.T) {
	var appended []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			appended = append(appended, msgs...)
			return nil
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	history, err := turnNudges{loop}.appendUserNudge(context.Background(), "s1", nil, "Rejected: cite evidence", st)
	testutil.FailErr(t, "appendUserNudge", err)
	if len(history) != 1 || history[0].Content != "Rejected: cite evidence" {
		t.Fatalf("history = %+v want one in-memory nudge", history)
	}
	if len(appended) != 0 {
		t.Fatalf("store append = %d want 0 while slot open", len(appended))
	}
	if len(st.deferredUserNudges) != 1 {
		t.Fatalf("deferred = %d want 1", len(st.deferredUserNudges))
	}
	if err := (turnNudges{loop}).closeCoordinatorDraftSlot(context.Background(), "s1", st, "slot-1"); err != nil {
		testutil.FailErr(t, "closeCoordinatorDraftSlot", err)
	}
	if len(appended) != 1 {
		t.Fatalf("store append after close = %d want 1", len(appended))
	}
	if appended[0].Content != "Rejected: cite evidence" {
		t.Fatalf("persisted nudge = %q", appended[0].Content)
	}
}

func TestAppendHostNudgeReplacesSameCode(t *testing.T) {
	var updated []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			return nil
		},
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			updated = append(updated, msg)
			return nil
		},
	})
	first := "Rejected: first\nCode: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nRun verify"
	history, err := turnNudges{loop}.appendHostNudge(context.Background(), "s1", nil, HostNudge{Content: first, Feedback: &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}}, "", nil)
	testutil.FailErr(t, "first nudge", err)
	if len(history) != 1 {
		t.Fatalf("history after first = %d", len(history))
	}
	second := "Rejected: second\nCode: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nUse command"
	history, err = turnNudges{loop}.appendHostNudge(context.Background(), "s1", history, HostNudge{Content: second, Feedback: &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}}, "", nil)
	testutil.FailErr(t, "second nudge", err)
	if len(history) != 1 {
		t.Fatalf("same-code kick must replace, history = %d", len(history))
	}
	if history[0].Content != second {
		t.Fatalf("replaced content = %q", history[0].Content)
	}
	if len(updated) != 1 || updated[0].Content != second {
		t.Fatalf("UpdateMessage = %+v", updated)
	}
}

func TestAppendHostNudgeSameCodeWhileSlotOpenPatchesStore(t *testing.T) {
	var appended []api.Message
	var updated []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			appended = append(appended, msgs...)
			return nil
		},
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			updated = append(updated, msg)
			return nil
		},
	})
	first := "Rejected: first\nCode: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nRun verify"
	history, err := turnNudges{loop}.appendHostNudge(context.Background(), "s1", nil, HostNudge{Content: first, Feedback: &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}}, "", nil)
	testutil.FailErr(t, "persist first", err)
	if len(appended) != 1 {
		t.Fatalf("first persist = %d want 1", len(appended))
	}
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	second := "Rejected: second\nCode: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nUse command"
	history, err = turnNudges{loop}.appendHostNudge(context.Background(), "s1", history, HostNudge{Content: second, Feedback: &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}}, "", st)
	testutil.FailErr(t, "same-code while slot open", err)
	if len(history) != 1 || history[0].Content != second {
		t.Fatalf("history = %+v", history)
	}
	if len(st.deferredUserNudges) != 0 {
		t.Fatalf("persisted row must not join deferred list: %d", len(st.deferredUserNudges))
	}
	if len(updated) != 1 || updated[0].Content != second {
		t.Fatalf("UpdateMessage = %+v", updated)
	}
	if err := (turnNudges{loop}).closeCoordinatorDraftSlot(context.Background(), "s1", st, "slot-1"); err != nil {
		testutil.FailErr(t, "close slot", err)
	}
	if len(appended) != 1 {
		t.Fatalf("flush re-appended persisted nudge: %d", len(appended))
	}
}

func TestFlushDeferredUserNudgesOneBatch(t *testing.T) {
	var batches []int
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			batches = append(batches, len(msgs))
			return nil
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	history, err := turnNudges{loop}.appendUserNudge(context.Background(), "s1", nil, "Rejected: a\nCode: ALPHA\n", st)
	testutil.FailErr(t, "alpha", err)
	_, err = turnNudges{loop}.appendUserNudge(context.Background(), "s1", history, "Rejected: b\nCode: BETA\n", st)
	testutil.FailErr(t, "beta", err)
	if err := (turnNudges{loop}).closeCoordinatorDraftSlot(context.Background(), "s1", st, "slot-1"); err != nil {
		testutil.FailErr(t, "close slot", err)
	}
	if len(batches) != 1 || batches[0] != 2 {
		t.Fatalf("flush batches = %v want one batch of 2", batches)
	}
}

func TestAppendHostNudgeKeepsDifferentCodes(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error { return nil },
	})
	history, err := turnNudges{loop}.appendUserNudge(context.Background(), "s1", nil, "Rejected: a\nCode: ALPHA\n", nil)
	testutil.FailErr(t, "alpha", err)
	history, err = turnNudges{loop}.appendUserNudge(context.Background(), "s1", history, "Rejected: b\nCode: BETA\n", nil)
	testutil.FailErr(t, "beta", err)
	if len(history) != 2 {
		t.Fatalf("different codes must both stay, history = %d", len(history))
	}
}

func TestFilterPromptHistoryKeepsToolGroupContiguousForWire(t *testing.T) {
	history := []api.Message{
		{
			ID:   "a1",
			Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{
				{ID: "call_a", Name: "grep"},
				{ID: "call_b", Name: "read"},
			},
		},
		{ID: "n1", Role: api.MessageRoleUser, Content: "[host:coordinator-citation-grounding]", Visibility: api.MessageVisibilityInternal},
		{ID: "t1", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "call_a", Tool: "grep"}, Content: "grep ok"},
		{ID: "t2", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "call_b", Tool: "read"}, Content: "read ok"},
	}
	out := api.FilterPromptHistory(history)
	if len(out) != 4 || out[3].ID != "n1" {
		t.Fatalf("filtered order = %v want nudge after tools", []string{out[0].ID, out[1].ID, out[2].ID, out[3].ID})
	}
}
