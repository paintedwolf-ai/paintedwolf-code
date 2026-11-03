package promptloop_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/promptresult"
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
	deps.Model.LLM = client
	deps.Context.Tools = tools.NewStubRegistry()
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Closeout.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string, _ bool, _ []string, _ bool) (*guidance.Refusal, bool) {
		return guidance.NewRefusal("SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT", "Required check is unresolved."), true
	}
	deps.Closeout.TurnCloseoutNudge = func(context.Context, *api.Session, string, promptloop.TurnCloseoutCause) promptloop.HostNudge {
		return promptloop.HostNudge{Content: "Report delivered work and the unresolved check."}
	}
	deps.Closeout.AssembleLedgerCloseout = func(_ context.Context, _, _ string, _ []string, drafted string, _ int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
		return guidance.CoordinatorCompletionReport{Synthesis: drafted}, &api.CitationGrounding{HostAssembled: true}
	}
	deps.Context.PromptTurnSurface = func(string) string { return "implement_investigate" }
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(t.Context(), promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("Make the edit"), ProfileID: "coordinator", ToolCtx: tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sess.ID},
	}})
	testutil.FailErr(t, "run rejected prose loop", err)
	if client.calls < 2 || client.calls > promptloop.BlockedLoopRejectCap+2 {
		t.Fatalf("model calls=%d; expected bounded recovery and a handoff", client.calls)
	}
	if result == nil || result.LastAssistantID == "" {
		t.Fatal("missing final handoff")
	}
}

// Exercise the actual request surface, not only the retry counter.
func TestGatedCloseoutGetsOneActingRepairRound(t *testing.T) {
	for _, barrier := range []bool{false, true} {
		for _, repaired := range []bool{false, true} {
			t.Run(fmt.Sprintf("barrier=%t/repaired=%t", barrier, repaired), func(t *testing.T) {
				messages := store.NewMemory()
				sess, err := messages.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "")
				testutil.FailErr(t, "create session", err)
				gateOpen := true
				registry := tools.NewStubRegistry()
				calls := 0
				testutil.FailErr(t, "register command", registry.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
					calls++
					gateOpen = false
					return "required evidence", nil
				}))
				acting := &modelcall.Completion{Content: "Still unresolved"}
				if repaired {
					acting = &modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "repair-read", Name: "command", Args: map[string]any{"command": "cat a.txt"}}}}
				}
				client := &sequentialLLMClient{completions: []*modelcall.Completion{{Content: "Premature closeout"}, acting, {Content: "Final report"}}}
				deps := promptloop.StoreDeps(messages)
				deps.Model.LLM = client
				deps.Context.Tools = registry
				deps.Context.Policy = &recordingToolPolicy{}
				deps.Context.Limits = loopTestLimits(1)
				deps.Context.CoordinatorFrame = investigateCoordinatorContext()
				refusal := func(needsRepair bool) *guidance.Refusal {
					cause := guidance.ErrCloseoutStopped
					if needsRepair {
						cause = errors.Join(cause, guidance.ErrCloseoutNeedsRepairTurn)
					}
					return guidance.NewRefusal("WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT", "Required workflow evidence remains open").
						WithDetails(map[string]any{"phase": "implement", "closeout_gate_open_leaves": "test, build"}, nil).
						WithCause(cause)
				}
				rejections := 0
				if barrier {
					deps.Closeout.AssertCompletion = func(context.Context, *api.Session) error {
						if gateOpen {
							rejections++
							return refusal(rejections == 1)
						}
						return nil
					}
				} else {
					deps.Closeout.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string, _ bool, _ []string, invoke bool) (*guidance.Refusal, bool) {
						if !gateOpen {
							return nil, false
						}
						rejections++
						if invoke {
							return guidance.NewRefusal("WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT", "Required workflow evidence remains open").
								WithDetails(map[string]any{"phase": "implement", "closeout_gate_open_leaves": "test, build"}, nil), true
						}
						return refusal(rejections == 1), true
					}
				}
				result, err := promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
					SessionID: sess.ID, Session: sess, History: userHistory("Complete the workflow"), ProfileID: "coordinator",
					ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}},
				})
				testutil.FailErr(t, "run gated closeout", err)
				if repaired {
					if result == nil || result.IsCloseoutBlocked() || calls != 1 {
						t.Fatalf("result=%+v calls=%d", result, calls)
					}
				} else {
					if result == nil || !result.IsCloseoutBlocked() || result.Outcome != promptresult.OutcomeCloseoutBlocked || calls != 0 {
						t.Fatalf("unresolved closeout result=%+v calls=%d", result, calls)
					}
					if result.CloseoutBlocked == nil ||
						result.CloseoutBlocked.Code != "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT" ||
						result.CloseoutBlocked.Phase != "implement" ||
						!slices.Equal(result.CloseoutBlocked.OpenGateLeaves, []string{"test", "build"}) ||
						!strings.Contains(result.CloseoutBlocked.Notice, "Workflow phase implement is waiting on open gate(s): test, build (WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT)") {
						t.Fatalf("closeout_blocked=%+v", result.CloseoutBlocked)
					}
					stored, err := messages.GetMessages(t.Context(), sess.ID)
					testutil.FailErr(t, "read transcript messages", err)
					var foundNotice bool
					for _, msg := range stored {
						if msg.Kind == api.MessageKindHostNudge && msg.Visibility == api.MessageVisibilityTranscript && msg.HostSignalID == "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT" && msg.Content == result.CloseoutBlocked.Notice {
							foundNotice = true
						}
					}
					if !foundNotice {
						t.Fatalf("expected transcript-visible closeout_blocked host notice in %+v", stored)
					}
				}
				if client.idx < 2 || client.idx > 3 || client.requests[0].ToolUse != modelcall.ToolUseForbidden || client.requests[1].ToolUse != modelcall.ToolUseAllowed || client.toolsPerRequest[1] == 0 {
					t.Fatalf("repair request tools=%v calls=%d", client.toolsPerRequest, client.idx)
				}
			})
		}
	}
}

func TestGatedCloseoutPathsSettleAsCloseoutBlocked(t *testing.T) {
	for _, path := range []string{"normal", "prose_finish", "completion_barrier"} {
		t.Run(path, func(t *testing.T) {
			messages := store.NewMemory()
			sess, err := messages.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "")
			testutil.FailErr(t, "create session", err)
			registry := tools.NewStubRegistry()
			testutil.FailErr(t, "register read", registry.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				return "partial evidence", nil
			}))
			client := &repeatedProseClient{}
			deps := promptloop.StoreDeps(messages)
			deps.Projection.Events = &events.Publisher{Hub: events.NewMemoryHub()}
			deps.Model.LLM = client
			deps.Context.Tools = registry
			deps.Context.Policy = &recordingToolPolicy{}
			maxIter := 4
			proseFinish := false
			if path == "prose_finish" {
				maxIter = 1
				proseFinish = true
			}
			deps.Context.Limits = loopTestLimits(maxIter)
			deps.Context.CoordinatorFrame = investigateCoordinatorContext()
			stoppedRefusal := func(needsRepair bool) *guidance.Refusal {
				cause := guidance.ErrCloseoutStopped
				if needsRepair {
					cause = errors.Join(cause, guidance.ErrCloseoutNeedsRepairTurn)
				}
				ref := guidance.NewRefusal("WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT", "Required workflow gate remains open").WithCause(cause)
				if path != "completion_barrier" || needsRepair {
					ref = ref.WithDetails(map[string]any{"phase": "verify", "closeout_gate_open_leaves": "lint"}, nil)
				}
				return ref
			}
			rejections := 0
			if path == "completion_barrier" {
				deps.Closeout.AssertCompletion = func(context.Context, *api.Session) error {
					rejections++
					if rejections == 3 {
						return guidance.NewRefusal("", "unreadable gate state").WithCause(guidance.ErrCloseoutStopped)
					}
					return stoppedRefusal(rejections == 1)
				}
			} else {
				deps.Closeout.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string, _ bool, _ []string, invoke bool) (*guidance.Refusal, bool) {
					rejections++
					if invoke && rejections <= 2 {
						return guidance.NewRefusal("WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT", "Required workflow gate remains open").
							WithDetails(map[string]any{"phase": "verify", "closeout_gate_open_leaves": "lint"}, nil), true
					}
					return stoppedRefusal(!invoke && rejections == 1), true
				}
			}
			result, err := promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
				SessionID: sess.ID, Session: sess, History: userHistory("Finish verify phase"), ProfileID: "coordinator",
				ProseFinish: proseFinish,
				ToolCtx:     tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}},
			})
			testutil.FailErr(t, "run closeout path "+path, err)
			if result == nil || !result.IsCloseoutBlocked() || result.CloseoutBlocked == nil {
				t.Fatalf("path=%s result=%+v", path, result)
			}
			if result.CloseoutBlocked.Code != "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT" {
				t.Fatalf("path=%s closeout_blocked=%+v", path, result.CloseoutBlocked)
			}
			if path != "completion_barrier" && (result.CloseoutBlocked.Phase != "verify" || !slices.Equal(result.CloseoutBlocked.OpenGateLeaves, []string{"lint"})) {
				t.Fatalf("path=%s closeout_blocked=%+v", path, result.CloseoutBlocked)
			}
			if path == "completion_barrier" {
				if _, err := promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
					SessionID: sess.ID, Session: sess, History: userHistory("Unreadable gate state"), ProfileID: "coordinator",
					ToolCtx: tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sess.ID}},
				}); err == nil {
					t.Fatal("expected unreadable gate state without code to fail closed")
				}
			}
		})
	}
}
