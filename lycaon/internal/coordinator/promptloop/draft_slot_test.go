package promptloop

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRejectLoopStableIDUsesDraftSlot(t *testing.T) {
	// A reject never closes the slot, so the open draft's id is stable across model
	// turns and the retry versions the same rail.
	st := &promptLoopTurnState{}
	slotA := st.ensureCoordinatorDraftSlot()
	slotB := st.ensureCoordinatorDraftSlot()
	if slotA != slotB || slotA == "" {
		t.Fatalf("slot ids = %q %q want same non-empty id", slotA, slotB)
	}
}

func TestCommittedStepClosesDraftSlotForFreshRow(t *testing.T) {
	// A committed step closes its slot so the next model turn opens its own row —
	// intermediate orchestration prose persists in place instead of being overwritten.
	st := &promptLoopTurnState{}
	slotA := st.ensureCoordinatorDraftSlot()
	st.draftSlotAppended = true
	st.closeCoordinatorDraftSlot(slotA)
	if st.draftSlotID != "" || st.draftSlotAppended {
		t.Fatalf("after close: id=%q appended=%v want empty/false", st.draftSlotID, st.draftSlotAppended)
	}
	slotB := st.ensureCoordinatorDraftSlot()
	if slotB == "" || slotB == slotA {
		t.Fatalf("slot ids = %q then %q want a fresh id after a committed step", slotA, slotB)
	}
}

func TestCloseDraftSlotIgnoresNonCurrentID(t *testing.T) {
	// Committing some other assistant row (e.g. a worker turn) must not close the
	// coordinator's open draft slot.
	st := &promptLoopTurnState{}
	slotA := st.ensureCoordinatorDraftSlot()
	st.draftSlotAppended = true
	st.closeCoordinatorDraftSlot("some-other-id")
	if st.draftSlotID != slotA || !st.draftSlotAppended {
		t.Fatalf("close(other) mutated slot: id=%q appended=%v want %q/true", st.draftSlotID, st.draftSlotAppended, slotA)
	}
}

func TestRejectEmitsNoDeleteForCoordinatorDraftSlot(t *testing.T) {
	var snapshotted int
	st := &promptLoopTurnState{}
	st.draftSlotID = "slot-1"
	st.draftSlotAppended = true
	loop := NewPromptLoopForTest(PromptLoopDeps{
		HintConfig: loadCoordinatorTestHintConfig(t),
		AppendDraftVersion: func(_ context.Context, _, slotID, body, code string) (int, error) {
			if slotID != "slot-1" || body != "bad synthesis" {
				t.Fatalf("snapshot slot=%q body=%q code=%q", slotID, body, code)
			}
			snapshotted++
			return 1, nil
		},
		UpdateMessage: func(_ context.Context, _, _ string, _ api.Message) error { return nil },
		AppendMessages: func(_ context.Context, _ string, msgs ...api.Message) error {
			return nil
		},
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "bad synthesis", Visibility: api.MessageVisibilityInternal},
	}
	_, err := loop.rejectBlockedAssistantTurn(
		context.Background(), "s1", history, "slot-1", "slot-1",
		refusalForTest("Rejected: test\nCode: SYNTH_HANDLE_NOT_IN_LEGS"),
		nil,
	)
	testutil.FailErr(t, "rejectBlockedAssistantTurn", err)
	if snapshotted != 1 {
		t.Fatalf("snapshotted = %d want 1", snapshotted)
	}
}

func TestRetryPromptExcludesRejectedProse(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendDraftVersion: func(_ context.Context, _, _, _, _ string) (int, error) { return 1, nil },
		UpdateMessage:      func(_ context.Context, _, _ string, _ api.Message) error { return nil },
		AppendMessages:     func(_ context.Context, _ string, _ ...api.Message) error { return nil },
	})
	history := []api.Message{
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "rejected prose", Visibility: api.MessageVisibilityInternal},
	}
	out, err := loop.retractRejectedAssistantTurn(context.Background(), "s1", history, "slot-1", "slot-1", refusalForTest("Code: TEST"))
	testutil.FailErr(t, "retractRejectedAssistantTurn", err)
	if len(out) != 1 {
		t.Fatalf("history len = %d want 1", len(out))
	}
	for _, msg := range out {
		if msg.Role == api.MessageRoleAssistant && strings.Contains(msg.Content, "rejected prose") {
			t.Fatalf("rejected prose still in history: %+v", out)
		}
	}
}

func TestRejectSnapshotsVersionsWithOutcomeCode(t *testing.T) {
	var code string
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendDraftVersion: func(_ context.Context, _, _, _, outcomeCode string) (int, error) {
			code = outcomeCode
			return 1, nil
		},
		UpdateMessage: func(_ context.Context, _, _ string, _ api.Message) error { return nil },
	})
	history := []api.Message{{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "v1"}}
	_, err := loop.retractRejectedAssistantTurn(
		context.Background(), "s1", history, "slot-1", "slot-1",
		refusalForTest("Rejected: x\nCode: COORDINATOR_UNGROUNDED_CLAIM"),
	)
	testutil.FailErr(t, "retractRejectedAssistantTurn", err)
	if code != "COORDINATOR_UNGROUNDED_CLAIM" {
		t.Fatalf("code = %q", code)
	}
}

func TestRetractKeepsRejectedBodyOnWireRow(t *testing.T) {
	var patched api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendDraftVersion: func(_ context.Context, _, _, _, _ string) (int, error) { return 1, nil },
		CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 1, nil },
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			patched = msg
			return nil
		},
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error { return nil },
	})
	history := []api.Message{
		{ID: "slot-1", Role: api.MessageRoleAssistant, Content: "rejected prose", Visibility: api.MessageVisibilityInternal},
	}
	_, err := loop.retractRejectedAssistantTurn(context.Background(), "s1", history, "slot-1", "slot-1", refusalForTest("Code: TEST"))
	testutil.FailErr(t, "retractRejectedAssistantTurn", err)
	if patched.Content != "rejected prose" {
		t.Fatalf("wire content = %q want rejected body kept (no blank draft rail)", patched.Content)
	}
	if patched.DraftStatus != api.DraftStatusLive {
		t.Fatalf("draft status = %q want live", patched.DraftStatus)
	}
	if patched.DraftVersionCount != 2 {
		t.Fatalf("count = %d want 2 (1 sidecar + live)", patched.DraftVersionCount)
	}
}

func TestFinalSynthesisCommitSettlesLiveDraftSlot(t *testing.T) {
	var patched api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 1, nil },
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			patched = msg
			return nil
		},
	})
	msg := api.Message{
		ID:          "slot-1",
		Role:        api.MessageRoleAssistant,
		Content:     "final answer",
		Grounding:   &api.CitationGrounding{},
		DraftStatus: api.DraftStatusLive,
	}
	committed, err := loop.commitGuardedAssistantTurn(
		context.Background(), &api.Session{ID: "s1"}, "s1", nil, "go", "implement_synthesis", msg,
	)
	testutil.FailErr(t, "commitGuardedAssistantTurn", err)
	if committed.Kind == api.MessageKindDraft {
		t.Fatalf("kind = draft; final synthesis must stay an assistant row")
	}
	if committed.DraftStatus != api.DraftStatusCommitted || patched.DraftStatus != api.DraftStatusCommitted {
		t.Fatalf("draft status = %q/%q want committed (never a stale live)", committed.DraftStatus, patched.DraftStatus)
	}
	if committed.DraftVersionCount != 2 {
		t.Fatalf("count = %d want 2", committed.DraftVersionCount)
	}
}

func TestToolBatchPromoteSettlesLiveMidRunStep(t *testing.T) {
	var patched api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 0, nil },
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			patched = msg
			return nil
		},
	})
	msg := api.Message{
		ID:          "slot-1",
		Role:        api.MessageRoleAssistant,
		DraftStatus: api.DraftStatusLive,
		ToolCalls:   []api.ToolCall{{ID: "call_1", Name: "read"}},
	}
	committed, err := loop.commitGuardedAssistantTurn(
		context.Background(), &api.Session{ID: "s1"}, "s1", nil, "go", "", msg,
	)
	testutil.FailErr(t, "commitGuardedAssistantTurn", err)
	if committed.Kind != "" {
		t.Fatalf("kind = %q want empty for tool-only mid-run step", committed.Kind)
	}
	if committed.DraftStatus != api.DraftStatusCommitted || patched.DraftStatus != api.DraftStatusCommitted {
		t.Fatalf("draft status = %q/%q want committed (never stale live)", committed.DraftStatus, patched.DraftStatus)
	}
}

func TestToolCallOnlyEmptyProseSettlesLiveMidRunStep(t *testing.T) {
	loop := NewPromptLoopForTest(PromptLoopDeps{
		CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 0, nil },
		UpdateMessage:      func(_ context.Context, _, _ string, _ api.Message) error { return nil },
	})
	msg := api.Message{
		ID:          "slot-1",
		Role:        api.MessageRoleAssistant,
		Content:     "",
		DraftStatus: api.DraftStatusLive,
		ToolCalls:   []api.ToolCall{{ID: "call_1", Name: "grep"}},
	}
	committed, err := loop.commitGuardedAssistantTurn(
		context.Background(), &api.Session{ID: "s1"}, "s1", nil, "go", "", msg,
	)
	testutil.FailErr(t, "commitGuardedAssistantTurn", err)
	if committed.Kind != "" {
		t.Fatalf("kind = %q want empty for tool-call-only step", committed.Kind)
	}
	if committed.DraftStatus != api.DraftStatusCommitted {
		t.Fatalf("draft status = %q want committed", committed.DraftStatus)
	}
}

func TestToolCallOnlyEmptyProseKeepsDraftKindWithRejectedVersions(t *testing.T) {
	// A tool-only retry that commits with empty prose after an earlier attempt was
	// rejected (draft_version_count > 1) must keep kind=draft, so Den renders the
	// rejected draft's version rail instead of collapsing the row to bare tool chips.
	loop := NewPromptLoopForTest(PromptLoopDeps{
		CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 1, nil },
		UpdateMessage:      func(_ context.Context, _, _ string, _ api.Message) error { return nil },
	})
	msg := api.Message{
		ID:          "slot-1",
		Role:        api.MessageRoleAssistant,
		Content:     "",
		DraftStatus: api.DraftStatusLive,
		ToolCalls:   []api.ToolCall{{ID: "call_1", Name: "grep"}},
	}
	committed, err := loop.commitGuardedAssistantTurn(
		context.Background(), &api.Session{ID: "s1"}, "s1", nil, "go", "", msg,
	)
	testutil.FailErr(t, "commitGuardedAssistantTurn", err)
	if committed.Kind != api.MessageKindDraft {
		t.Fatalf("kind = %q want draft (rejected version rail must survive)", committed.Kind)
	}
	if committed.DraftVersionCount != 2 {
		t.Fatalf("count = %d want 2 (1 sidecar + live)", committed.DraftVersionCount)
	}
	if committed.DraftStatus != api.DraftStatusCommitted {
		t.Fatalf("draft status = %q want committed", committed.DraftStatus)
	}
}

func TestWithdrawnTerminalState(t *testing.T) {
	var patched api.Message
	st := &promptLoopTurnState{
		draftSlotID:          "slot-1",
		draftSlotAppended:    true,
		lastAssistantContent: "never committed",
	}
	loop := NewPromptLoopForTest(PromptLoopDeps{
		CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 0, nil },
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			patched = msg
			return nil
		},
	})
	err := loop.maybeWithdrawCoordinatorDraft(context.Background(), &api.Session{ID: "s1"}, "s1", st)
	testutil.FailErr(t, "maybeWithdrawCoordinatorDraft", err)
	if patched.Kind != api.MessageKindDraft || patched.DraftStatus != api.DraftStatusWithdrawn {
		t.Fatalf("patched = %+v want withdrawn draft", patched)
	}
	if patched.Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("visibility = %q want transcript", patched.Visibility)
	}
}

func TestVersionCountPopulatedOnCommit(t *testing.T) {
	var patched api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		CountDraftVersions: func(_ context.Context, _, _ string) (int, error) { return 2, nil },
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			patched = msg
			return nil
		},
	})
	msg := api.Message{ID: "slot-1", Content: "final", Role: api.MessageRoleAssistant}
	committed, err := loop.commitGuardedAssistantTurn(
		context.Background(), &api.Session{ID: "s1"}, "s1", nil, "go", "implement_dispatch", msg,
	)
	testutil.FailErr(t, "commitGuardedAssistantTurn", err)
	if committed.DraftVersionCount != 3 {
		t.Fatalf("count = %d want 3 (2 sidecar + live)", committed.DraftVersionCount)
	}
	if patched.DraftVersionCount != 3 {
		t.Fatalf("patched count = %d", patched.DraftVersionCount)
	}
}

func TestNonSlotRejectSupersedesInPlace(t *testing.T) {
	var patched api.Message
	loop := NewPromptLoopForTest(PromptLoopDeps{
		UpdateMessage: func(_ context.Context, _, _ string, msg api.Message) error {
			patched = msg
			return nil
		},
		AppendMessages:     func(_ context.Context, _ string, _ ...api.Message) error { return nil },
		AppendDraftVersion: func(context.Context, string, string, string, string) (int, error) { return 1, nil },
	})
	history := []api.Message{{ID: "a1", Role: api.MessageRoleAssistant, Content: "worker bad", Ord: 3}}
	_, err := loop.rejectBlockedAssistantTurn(context.Background(), "s1", history, "a1", "", refusalForTest("Code: TEST"), nil)
	testutil.FailErr(t, "rejectBlockedAssistantTurn", err)
	if patched.ID != "a1" {
		t.Fatalf("patched id = %q want a1", patched.ID)
	}
	if patched.DraftStatus != api.DraftStatusRejected {
		t.Fatalf("draft status = %q want rejected", patched.DraftStatus)
	}
	if patched.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("visibility = %q want internal", patched.Visibility)
	}
}
