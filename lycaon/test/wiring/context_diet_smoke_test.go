package wiring

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestContextDietRoutingToTaskDispatch(t *testing.T) {
	var stage atomic.Int32
	mock := llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(_ context.Context, _ llm.Snapshot) modelcall.Completion {
			if stage.Load() == 0 {
				stage.Store(1)
				return modelcall.Completion{
					ToolCalls: []wire.ToolCall{{
						ID: "t-route", Name: "task",
						Args: TaskToolArgs("path-explorer", "Map the repo layout."),
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

	if _, err := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Map this codebase"); err != nil {
		testutil.FailErr(t, "Prompt", err)
	}
	queued, err := h.Workers.Queue.ListBySession(ctx, sess.ProjectID, sess.ID)
	testutil.FailErr(t, "list session workers", err)
	if len(queued) != 1 || queued[0].AgentType != "path-explorer" {
		var results []any
		for _, msg := range mustMessages(t, h, ctx, sess.ID) {
			if msg.ToolResult != nil {
				results = append(results, msg.ToolResult.Tool, msg.ToolResult.Outcome, msg.ToolResult.Feedback)
			}
		}
		t.Fatalf("routing turn dispatched %+v, want one path-explorer worker; tool results %v", queued, results)
	}
}
