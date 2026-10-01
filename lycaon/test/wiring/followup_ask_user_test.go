package wiring

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFollowUpAfterCompletedWorkflowCanAskAndReceiveAnswer(t *testing.T) {
	var calls atomic.Int32
	mock := llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(context.Context, llm.Snapshot) modelcall.Completion {
			if calls.Add(1) > 1 {
				return modelcall.Completion{Content: MockCoordinatorCloseoutJSON("Question was not parked.", "")}
			}
			return modelcall.Completion{ToolCalls: []api.ToolCall{{
				ID: "followup-platform", Name: "ask_user",
				Args: map[string]any{
					"prompt":        "Which platform should I build for?",
					"response_type": "single_choice",
					"options":       []any{"macOS", "iOS"},
				},
			}}}
		},
	})
	h := BuildForTest(t, WithLLMClient(mock), WithoutCoordinatorLoop())
	ctx := h.OwnerCtx(t, t.Context())
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, t.TempDir())
	testutil.FailErr(t, "create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	original, err := h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "load original workflow", err)
	if original == nil {
		t.Fatal("original workflow missing")
	}
	// Completed history leaves the follow-up without an active workflow.
	original.Status = api.WorkflowRunStatusComplete
	completedAt := time.Now().UTC()
	original.CompletedAt = &completedAt
	testutil.FailErr(t, "complete original workflow", h.WorkflowMgr.Store.Update(ctx, original))
	active, err := h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "check workflow gap", err)
	if active != nil {
		t.Fatal("fixture must have no active workflow before the follow-up")
	}

	_, err = h.SessionMgr.Prompt(ctx, sess.ID, "Start again with a native Swift game.")
	testutil.FailErr(t, "submit follow-up", err)
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want one call then park on the question", calls.Load())
	}
	active, err = h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "load follow-up workflow", err)
	if active == nil || active.ID == original.ID || !h.WorkflowMgr.IsAmbientRun(active) {
		t.Fatalf("follow-up workflow = %+v, want a fresh ambient run", active)
	}
	ui, err := h.WorkflowMgr.ComputeRunUI(ctx, active)
	testutil.FailErr(t, "project question card", err)
	if ui.PendingFeedback == nil || ui.PendingFeedback.Prompt != "Which platform should I build for?" {
		t.Fatalf("pending question = %+v", ui.PendingFeedback)
	}
	phaseID := ui.PendingFeedback.PhaseID
	assertFollowUpAskResult(t, h, sess.ID, "pending", "")
	assertFollowUpAskCard(t, h, sess.ID, active.ID, phaseID)

	_, err = h.WorkflowMgr.ResolveUserDecision(ctx, sess.ID, active.ID, phaseID, []string{"macOS"}, "")
	testutil.FailErr(t, "answer follow-up question", err)
	assertFollowUpAskResult(t, h, sess.ID, "answered", "macOS")
	vars, err := h.WorkflowMgr.Store.GetScaffoldVars(ctx, active.ID)
	testutil.FailErr(t, "load answered workflow", err)
	if scaffoldvars.HasPendingUserInput(vars) {
		t.Fatal("answered question still blocks the workflow")
	}
	testutil.FailErr(t, "answered workflow runnable", h.WorkflowMgr.AssertSessionRunnable(ctx, sess.ID))
	old, err := h.WorkflowMgr.Get(ctx, original.ID)
	testutil.FailErr(t, "load original history", err)
	if old.Status != api.WorkflowRunStatusComplete || !old.CompletedAt.Equal(completedAt) {
		t.Fatalf("follow-up changed completed history: %+v", old)
	}
}

func assertFollowUpAskResult(t *testing.T, h *Harness, sessionID, status, response string) {
	t.Helper()
	messages, err := h.Store.GetMessages(t.Context(), sessionID)
	testutil.FailErr(t, "load question result", err)
	for _, message := range messages {
		result := message.ToolResult
		if result == nil || result.ToolCallID != "followup-platform" {
			continue
		}
		var body struct {
			Status   string `json:"status"`
			Response string `json:"response"`
		}
		testutil.FailErr(t, "decode question result", json.Unmarshal([]byte(result.Content), &body))
		if len(result.Codes) != 0 || body.Status != status || body.Response != response {
			t.Fatalf("question result = %+v, codes = %v; want %s / %q", body, result.Codes, status, response)
		}
		return
	}
	t.Fatal("original question tool result missing")
}

func assertFollowUpAskCard(t *testing.T, h *Harness, sessionID, runID, phaseID string) {
	t.Helper()
	messages, err := h.Store.GetMessages(t.Context(), sessionID)
	testutil.FailErr(t, "load question card", err)
	for _, message := range messages {
		if card := message.WorkflowFeedback; card != nil && card.PhaseID == phaseID {
			if message.WorkflowRunID != runID {
				t.Fatalf("question run = %q, want %q", message.WorkflowRunID, runID)
			}
			return
		}
	}
	t.Fatal("question card missing from transcript")
}
