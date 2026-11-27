package wiring

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestContextDietRoutingToTaskDispatch(t *testing.T) {
	var stage atomic.Int32
	mock := llmtest.NewKickDrivenMock(llmtest.KickDrivenConfig{
		Fallback: func(_ context.Context, _ llmtest.Snapshot) modelcall.Completion {
			if stage.Load() == 0 {
				stage.Store(1)
				return modelcall.Completion{
					ToolCalls: []wire.ToolCall{{
						ID: "t-route", Name: "task",
						Args: TaskToolArgs("implementer", "Map the repo layout into docs/layout.md."),
					}},
				}
			}
			return modelcall.Completion{Content: "Done."}
		},
	})

	h := BuildForTest(t, WithLLMClient(mock))
	ctx := context.Background()
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "Create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)
	testutil.FailErr(t, "board orient", h.Workflows.Manager.Fanout.RecordBoardOrientReady(ctx, sess.ID, "diet-board"))
	// Setup advances the workflow; its wake must not open a turn before the
	// chat holds a request, whichever runs first.
	loop := h.Sessions.Manager.Coordinator.Runtime.CoordinatorLoop()
	loop.Nudges.Nudge(ctx, sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
	loop.Turns.WaitForAsyncTurns(ctx)
	if stage.Load() != 0 {
		t.Fatalf("a host wake ran a model turn before the chat held a request; tool results %v", dietToolResults(t, h, ctx, sess.ID))
	}

	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Map this codebase"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}
	queued, err := h.Workers.Queue.ListBySession(ctx, sess.ProjectID, sess.ID)
	testutil.FailErr(t, "list session workers", err)
	if len(queued) != 1 || queued[0].AgentType != "implementer" {
		t.Fatalf("routing turn dispatched %+v, want one implementer worker; tool results %v", queued, dietToolResults(t, h, ctx, sess.ID))
	}
}

func dietToolResults(t *testing.T, h *Harness, ctx context.Context, sessionID string) []any {
	t.Helper()
	var results []any
	for _, msg := range mustMessages(t, h, ctx, sessionID) {
		if msg.ToolResult != nil {
			results = append(results, msg.ToolResult.Tool, msg.ToolResult.Outcome, msg.ToolResult.Feedback)
		}
	}
	return results
}
