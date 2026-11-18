package promptloop

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Parallel results finish out of call order; the history the next request
// sees must be the history a reload reads, including the evidence sample.
func TestParallelBatchLiveHistoryMatchesReload(t *testing.T) {
	ctx := t.Context()
	mem := store.NewMemory()
	deps := StoreDeps(mem)
	deps.Tools.CommitEvidenceToolResult = func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (string, string, error) {
		return mem.CommitVisualEvidenceToolResult(ctx, sessionID, sess.WorkspacePath, toolName, args, content, artifactID)
	}
	loop := NewPromptLoopForTest(deps)
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	prompt := api.Message{ID: "prompt", Role: api.MessageRoleUser, Content: "survey"}
	testutil.FailErr(t, "append prompt", mem.AppendMessages(ctx, sess.ID, prompt))

	paths := []string{"a.go", "b.go", "c.go"}
	calls := make([]api.ToolCall, len(paths))
	outcomes := make([]toolCallOutcome, len(paths))
	for i, path := range paths {
		calls[i] = api.ToolCall{ID: "call-" + path, Name: "read", Args: map[string]any{"path": path}}
		outcomes[i] = toolCallOutcome{
			index: i, toolName: "read", toolArgs: calls[i].Args, handleEligible: true,
			toolMsg: api.Message{ID: "result-" + path, Role: api.MessageRoleTool, Content: "contents of " + path,
				ToolResult: &api.ToolResult{ToolCallID: calls[i].ID, Tool: "read", Content: "contents of " + path, Outcome: api.ToolResultOutcomeCompleted}},
		}
	}
	last := time.Now().UTC()
	commit := newParallelBatchCommit([]api.Message{prompt}, calls, &last, nil, 0, &promptLoopTurnState{}, sess, sess.ID)
	// The last call finishes first.
	for i := len(outcomes) - 1; i >= 0; i-- {
		loop.Batch.appendParallelResult(ctx, commit, &outcomes[i])
	}
	testutil.FailErr(t, "finish batch", loop.Batch.finishParallelToolOutcomes(ctx, commit, outcomes))

	reloaded, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "reload", err)
	ids := func(history []api.Message) []string {
		out := make([]string, len(history))
		for i, msg := range history {
			out[i] = msg.ID
		}
		return out
	}
	if live, stored := ids(commit.history), ids(reloaded); !slices.Equal(live, stored) {
		t.Fatalf("live history %v, reload %v", live, stored)
	}
	if sample := guidance.IntentWindowHandles(reloaded, len(paths)); len(sample) != len(paths) {
		t.Fatalf("evidence sample %v, want one handle per read", sample)
	}
	for limit := 1; limit <= len(paths); limit++ {
		if live, stored := guidance.IntentWindowHandles(commit.history, limit), guidance.IntentWindowHandles(reloaded, limit); !slices.Equal(live, stored) {
			t.Fatalf("evidence sample of %d: live %v, reload %v", limit, live, stored)
		}
	}
}
