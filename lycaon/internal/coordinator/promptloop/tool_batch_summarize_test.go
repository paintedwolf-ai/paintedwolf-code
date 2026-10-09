package promptloop_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestLoopSummarizeParallelCappedInOneTurn verifies bounded parallel execution.
func TestLoopSummarizeParallelCappedInOneTurn(t *testing.T) {
	reg := tools.NewStubRegistry()
	var concurrent int32
	var peak int32
	_ = reg.Register("summarize", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		cur := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur > old && atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
			if cur <= old {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		return `{"brief":["ok"],"anchors":[],"task":"t"}`, nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("survey", []llm.MockToolCall{
		{ID: "tc1", Name: "summarize", Args: map[string]any{"task": "first", "path": "a.go"}},
		{ID: "tc2", Name: "summarize", Args: map[string]any{"task": "second", "path": "b.go"}},
	})})
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Tools.CommitEvidenceToolResult = commitEvidenceFromStore(store)
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := t.Context()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("survey"),
		ProfileID: "explore_readonly",
	})
	testutil.FailErr(t, "loop.Run", err)
	peakConcurrent := atomic.LoadInt32(&peak)
	if peakConcurrent < 2 {
		t.Fatalf("peak concurrent summarizes = %d want >=2", peakConcurrent)
	}
	if peakConcurrent > 4 {
		t.Fatalf("peak concurrent summarizes = %d exceeds cap 4", peakConcurrent)
	}
}
