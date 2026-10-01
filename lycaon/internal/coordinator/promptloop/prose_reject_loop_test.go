package promptloop_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type repeatedProseClient struct{ calls int }

func (c *repeatedProseClient) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.calls++
	return &modelcall.Completion{Content: "The edits are delivered; the check remains blocked."}, nil
}

func (c *repeatedProseClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	completion, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: completion.Content, Done: true}
	close(ch)
	return ch, nil
}

func TestRepeatedProseRejectionEndsWithBoundedHandoff(t *testing.T) {
	messages := store.NewMemory()
	sess, err := messages.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "")
	testutil.FailErr(t, "create session", err)
	client := &repeatedProseClient{}
	deps := promptloop.StoreDeps(messages)
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string, _ bool, _ []string, _ bool) (*guidance.Refusal, bool) {
		return guidance.NewRefusal("SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT", "Required check is unresolved."), true
	}
	deps.TurnCloseoutNudge = func(context.Context, *api.Session, string, promptloop.TurnCloseoutReason, string) promptloop.HostNudge {
		return promptloop.HostNudge{Content: "Report delivered work and the unresolved check."}
	}
	deps.AssembleLedgerCloseout = func(_ context.Context, _, _ string, _ []string, drafted string, _ int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
		return guidance.CoordinatorCompletionReport{Synthesis: drafted}, &api.CitationGrounding{HostAssembled: true}
	}
	deps.PromptTurnSurface = func(string) string { return "implement_investigate" }
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(t.Context(), promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("Make the edit"), ProfileID: "coordinator", ToolCtx: tools.ToolContext{SessionID: sess.ID}})
	testutil.FailErr(t, "run rejected prose loop", err)
	if client.calls < 2 || client.calls > promptloop.BlockedLoopRejectCap+2 {
		t.Fatalf("model calls=%d; expected bounded recovery and a handoff", client.calls)
	}
	if result == nil || result.LastAssistantID == "" {
		t.Fatal("missing final handoff")
	}
}
