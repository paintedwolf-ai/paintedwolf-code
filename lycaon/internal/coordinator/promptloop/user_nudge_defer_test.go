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
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				appended = append(appended, msgs...)
				return nil
			},
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	history, err := loop.Nudges.appendHostNudge(context.Background(), "s1", nil, HostNudge{Content: "Rejected: cite evidence"}, "", st)
	testutil.FailErr(t, "append host nudge", err)
	if len(history) != 1 || history[0].Content != "Rejected: cite evidence" {
		t.Fatalf("history = %+v want one in-memory nudge", history)
	}
	if len(appended) != 0 {
		t.Fatalf("store append = %d want 0 while slot open", len(appended))
	}
	if len(st.deferredUserNudges) != 1 {
		t.Fatalf("deferred = %d want 1", len(st.deferredUserNudges))
	}
	if err := loop.Nudges.closeCoordinatorDraftSlot(context.Background(), "s1", st, "slot-1"); err != nil {
		testutil.FailErr(t, "closeCoordinatorDraftSlot", err)
	}
	if len(appended) != 1 {
		t.Fatalf("store append after close = %d want 1", len(appended))
	}
	if appended[0].Content != "Rejected: cite evidence" {
		t.Fatalf("persisted nudge = %q", appended[0].Content)
	}
}

func TestAppendHostNudgeSupersedesSameCode(t *testing.T) {
	var updated []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				return nil
			},
			UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
				updated = append(updated, msg)
				return nil
			},
		},
	})
	first := "Rejected: first\nCode: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nRun verify"
	history, err := loop.Nudges.appendHostNudge(context.Background(), "s1", nil, HostNudge{Content: first, Feedback: &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}}, "", nil)
	testutil.FailErr(t, "first nudge", err)
	second := "Rejected: second\nCode: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nUse command"
	history, err = loop.Nudges.appendHostNudge(context.Background(), "s1", history, HostNudge{Content: second, Feedback: &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}}, "", nil)
	testutil.FailErr(t, "second nudge", err)
	if len(history) != 2 || history[0].DraftStatus != api.DraftStatusWithdrawn || history[1].Content != second {
		t.Fatalf("history = %+v want withdrawn first and active second", history)
	}
	if len(updated) != 1 || updated[0].Content != first || updated[0].DraftStatus != api.DraftStatusWithdrawn {
		t.Fatalf("UpdateMessage = %+v want withdrawn first row", updated)
	}
	prompt := api.FilterPromptHistory(history)
	if len(prompt) != 1 || prompt[0].Content != second {
		t.Fatalf("prompt history = %+v want only the latest repair", prompt)
	}
}

func TestAppendHostNudgeSupersedesStoredRowWhileSlotOpen(t *testing.T) {
	var appended []api.Message
	var updated []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				appended = append(appended, msgs...)
				return nil
			},
			UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
				updated = append(updated, msg)
				return nil
			},
		},
	})
	first := "Rejected: first\nCode: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nRun verify"
	history, err := loop.Nudges.appendHostNudge(context.Background(), "s1", nil, HostNudge{Content: first, Feedback: &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}}, "", nil)
	testutil.FailErr(t, "persist first", err)
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	second := "Rejected: second\nCode: SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT\nUse command"
	history, err = loop.Nudges.appendHostNudge(context.Background(), "s1", history, HostNudge{Content: second, Feedback: &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}}, "", st)
	testutil.FailErr(t, "same-code while slot open", err)
	if len(history) != 2 || history[0].DraftStatus != api.DraftStatusWithdrawn || history[1].Content != second {
		t.Fatalf("history = %+v", history)
	}
	if len(updated) != 1 || updated[0].DraftStatus != api.DraftStatusWithdrawn {
		t.Fatalf("UpdateMessage = %+v want the stored row withdrawn", updated)
	}
	if len(st.deferredUserNudges) != 1 {
		t.Fatalf("deferred = %d want the replacement", len(st.deferredUserNudges))
	}
	testutil.FailErr(t, "close slot", loop.Nudges.closeCoordinatorDraftSlot(context.Background(), "s1", st, "slot-1"))
	if len(appended) != 2 || appended[1].Content != second {
		t.Fatalf("appended = %+v want the replacement after the stored row", appended)
	}
}

func TestAppendHostNudgeDropsDeferredRowItSupersedes(t *testing.T) {
	var appended []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				appended = append(appended, msgs...)
				return nil
			},
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	feedback := &api.ToolFeedback{Code: "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"}
	history, err := loop.Nudges.appendHostNudge(context.Background(), "s1", nil, HostNudge{Content: "first", Feedback: feedback}, "", st)
	testutil.FailErr(t, "first", err)
	history, err = loop.Nudges.appendHostNudge(context.Background(), "s1", history, HostNudge{Content: "second", Feedback: feedback}, "", st)
	testutil.FailErr(t, "second", err)
	if len(history) != 1 || history[0].Content != "second" || len(st.deferredUserNudges) != 1 {
		t.Fatalf("history = %+v deferred = %d want only the replacement", history, len(st.deferredUserNudges))
	}
	testutil.FailErr(t, "close slot", loop.Nudges.closeCoordinatorDraftSlot(context.Background(), "s1", st, "slot-1"))
	if len(appended) != 1 || appended[0].Content != "second" {
		t.Fatalf("appended = %+v want only the replacement", appended)
	}
}

func TestFlushDeferredUserNudgesOneBatch(t *testing.T) {
	var batches []int
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				batches = append(batches, len(msgs))
				return nil
			},
		},
	})
	st := &promptLoopTurnState{draftSlotID: "slot-1", draftSlotAppended: true}
	history, err := loop.Nudges.appendHostNudge(context.Background(), "s1", nil, HostNudge{Content: "Rejected: a\nCode: ALPHA\n"}, "", st)
	testutil.FailErr(t, "alpha", err)
	_, err = loop.Nudges.appendHostNudge(context.Background(), "s1", history, HostNudge{Content: "Rejected: b\nCode: BETA\n"}, "", st)
	testutil.FailErr(t, "beta", err)
	if err := loop.Nudges.closeCoordinatorDraftSlot(context.Background(), "s1", st, "slot-1"); err != nil {
		testutil.FailErr(t, "close slot", err)
	}
	if len(batches) != 1 || batches[0] != 2 {
		t.Fatalf("flush batches = %v want one batch of 2", batches)
	}
}

func TestAppendHostNudgeKeepsDifferentCodes(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error { return nil },
		},
	})
	history, err := loop.Nudges.appendHostNudge(context.Background(), "s1", nil, HostNudge{Content: "Rejected: a\nCode: ALPHA\n"}, "", nil)
	testutil.FailErr(t, "alpha", err)
	history, err = loop.Nudges.appendHostNudge(context.Background(), "s1", history, HostNudge{Content: "Rejected: b\nCode: BETA\n"}, "", nil)
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

func TestDraftRecoveryPersistsBeforeRetryAndKeepsOneActiveRepair(t *testing.T) {
	var stored []api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{Projection: ProjectionDeps{
		AppendMessages: func(_ context.Context, _ string, messages ...api.Message) error {
			stored = append(stored, messages...)
			return nil
		},
		UpdateMessage: func(_ context.Context, _, _ string, _ api.Message) error { return nil },
	}})
	st := &promptLoopTurnState{draftSlotID: "draft", draftSlotAppended: true}
	nudge := HostNudge{DraftID: "draft", Content: "retry", Feedback: &api.ToolFeedback{Code: "TEST_RECOVERY"}}
	history, err := loop.Nudges.appendHostNudge(t.Context(), "session", nil, nudge, api.MessageKindCoordinatorGuidance, st)
	testutil.FailErr(t, "persist recovery", err)
	if len(stored) != 1 || len(st.deferredUserNudges) != 0 {
		t.Fatal("recovery must survive interruption before the retry finishes")
	}
	nudge.Content = "revised recovery"
	history, err = loop.Nudges.appendHostNudge(t.Context(), "session", history, nudge, api.MessageKindCoordinatorGuidance, st)
	testutil.FailErr(t, "supersede same draft recovery", err)
	nudge.DraftID = "later-draft"
	nudge.Content = "later recovery"
	history, err = loop.Nudges.appendHostNudge(t.Context(), "session", history, nudge, api.MessageKindCoordinatorGuidance, st)
	testutil.FailErr(t, "append later draft recovery", err)
	if len(stored) != 3 {
		t.Fatalf("stored = %d want every repair retained for audit", len(stored))
	}
	active := 0
	for _, msg := range history {
		if api.IsActiveRepair(msg) {
			active++
			if msg.Content != "later recovery" {
				t.Fatalf("active repair = %q want the latest", msg.Content)
			}
		}
	}
	if active != 1 {
		t.Fatalf("active repairs = %d want 1", active)
	}
}

func TestAppendAndFlushHostNudgesStampCanonicalOrdinals(t *testing.T) {
	var nextOrd int64
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Projection: ProjectionDeps{
			AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
				for i := range msgs {
					nextOrd++
					msgs[i].Ord = nextOrd
					msgs[i].Seq = nextOrd
				}
				return nil
			},
		},
	})
	st := &promptLoopTurnState{}
	var err error
	st.history, err = loop.Nudges.appendHostNudge(t.Context(), "s1", st.history, HostNudge{Content: "Immediate nudge\nCode: IMMEDIATE\n"}, "", st)
	testutil.FailErr(t, "append immediate nudge", err)
	if len(st.history) != 1 || st.history[0].Ord != 1 || st.history[0].Seq != 1 {
		t.Fatalf("immediate nudge = (ord=%d, seq=%d), want (1, 1)", st.history[0].Ord, st.history[0].Seq)
	}

	st.draftSlotID = "slot-1"
	st.draftSlotAppended = true
	st.history, err = loop.Nudges.appendHostNudge(t.Context(), "s1", st.history, HostNudge{Content: "Deferred nudge\nCode: DEFERRED\n"}, "", st)
	testutil.FailErr(t, "append deferred nudge", err)
	if len(st.history) != 2 || st.history[1].Ord != 0 {
		t.Fatalf("deferred nudge before flush = %+v, want Ord=0", st.history[1])
	}
	testutil.FailErr(t, "close draft slot and flush deferred nudges", loop.Nudges.closeCoordinatorDraftSlot(t.Context(), "s1", st, "slot-1"))
	if st.history[1].Ord != 2 || st.history[1].Seq != 2 {
		t.Fatalf("flushed deferred nudge in history = (ord=%d, seq=%d), want (2, 2)", st.history[1].Ord, st.history[1].Seq)
	}
}
