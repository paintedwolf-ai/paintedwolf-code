package promptloop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/spendguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTurnCloseoutSpendCeilingReason(t *testing.T) {
	got := promptloop.TurnCloseoutReasonText(promptloop.TurnCloseoutSpendCeiling)
	if got != "the session spend ceiling was reached" {
		t.Fatalf("reason text = %q", got)
	}
}

func TestPromptLoopSpendCeilingMapsToCloseout(t *testing.T) {
	if promptloop.TurnCloseoutSpendCeiling != "session_spend_ceiling" {
		t.Fatalf("closeout reason = %q", promptloop.TurnCloseoutSpendCeiling)
	}
	err := &spendguard.CeilingReached{CeilingUSD: 1, SpentUSD: 1}
	if !errors.Is(err, spendguard.ErrCeiling) {
		t.Fatal("typed ceiling error must unwrap to ErrSessionSpendCeiling")
	}
	_ = context.Background()
	_ = api.Session{}
}

func TestWorkerTerminalTurnRecordsOnlyAcceptedStructuredCompletion(t *testing.T) {
	for _, status := range []string{"complete", "partial", "blocked", "prose"} {
		t.Run(status, func(t *testing.T) {
			memory := store.NewMemory()
			sess, err := memory.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create worker session", err)
			sess.ParentSessionID, sess.AgentType = "parent", "implementer"
			args := map[string]any{"leg_status": status, "brief": "bounded work"}
			completion := &modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "finish", Name: workertools.CompleteLegTool, Args: args}}}
			if status == "prose" {
				completion = &modelcall.Completion{Content: `{"leg_status":"complete","brief":"unsupported assertion"}`}
			}
			client := &sequentialLLMClient{completions: []*modelcall.Completion{completion}}
			production := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register completion decoder", native.RegisterCompleteLegTool(production, workercompletion.CompleteLegDecoder))
			def, ok := production.Definition(workertools.CompleteLegTool)
			if !ok {
				t.Fatal("missing completion definition")
			}
			registry := tools.NewStubRegistry()
			testutil.FailErr(t, "register completion handler", registry.Register(workertools.CompleteLegTool, def.Handler))
			testutil.FailErr(t, "register mutation sentinel", registry.Register("write", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
				t.Error("terminal turn executed a source mutation")
				return "", nil
			}))
			deps := promptloop.StoreDeps(memory)
			deps.Model.LLM, deps.Context.Tools = client, registry
			deps.Context.Policy = &fixedToolPolicy{metas: []tools.ToolMeta{{Name: "write"}, {Name: workertools.CompleteLegTool}}}
			_, err = promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
				SessionID: sess.ID, Session: sess, History: userHistory("finish"), ProfileID: "implementer", ProseFinish: true,
				ToolCtx: tools.ToolContext{
					Identity: tools.InvocationIdentity{SessionID: sess.ID,
						ParentSessionID: sess.ParentSessionID,
						Agent:           sess.AgentType},
				},
			})
			testutil.FailErr(t, "run terminal turn", err)
			if len(client.requests) != 1 || len(client.requests[0].Tools) != 1 || client.requests[0].Tools[0].Name != workertools.CompleteLegTool {
				t.Fatal("terminal tool surface is not complete_leg only")
			}
			messages, err := memory.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read terminal transcript", err)
			report, _, recorded := workercompletion.LastCompleteLegReport(messages)
			if recorded != (status != "prose") || (recorded && report.LegStatus != status) {
				t.Fatalf("status=%s recorded=%v report=%+v", status, recorded, report)
			}
		})
	}
}

func TestProseCompletionRequiresUsablePayload(t *testing.T) {
	for _, forced := range []bool{false, true} {
		for _, content := range []string{"", " \n", "The turn limit prevented reading the file."} {
			t.Run(proseCompletionCaseName(forced, content), func(t *testing.T) {
				messages := store.NewMemory()
				sess, err := messages.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, "project")
				testutil.FailErr(t, "create session", err)
				calls := 0
				registry := tools.NewStubRegistry()
				testutil.FailErr(t, "register read", registry.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
					calls++
					return "file bytes", nil
				}))
				client := &sequentialLLMClient{completions: []*modelcall.Completion{{
					Content:   content,
					ToolCalls: []api.ToolCall{{ID: "read-1", Name: "read", Args: map[string]any{"path": "a.txt"}}},
				}}}
				deps := promptloop.StoreDeps(messages)
				deps.Model.LLM = client
				deps.Context.Tools = registry
				deps.Context.Policy = &recordingToolPolicy{}
				deps.Context.Limits = loopTestLimits(1)
				deps.Context.CoordinatorFrame = investigateCoordinatorContext()
				result, err := promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
					SessionID: sess.ID, Session: sess, History: userHistory("Read a.txt"),
					UserPrompt: "Read a.txt", ProfileID: "coordinator", ProseFinish: forced,
				})
				if content == "" || content == " \n" {
					// The provider answered; the model's final-turn call is its own miss.
					var miss *promptloop.ProseTurnToolCallError
					if !errors.As(err, &miss) || result != nil || len(miss.Tools) != 1 || miss.Tools[0] != "read" {
						t.Fatalf("tool-only prose result=%+v error=%v", result, err)
					}
					if errors.Is(err, failure.ErrProviderEmptyCompletion) {
						t.Fatalf("model's miss attributed to the provider: %v", err)
					}
				} else {
					testutil.FailErr(t, "run prose completion", err)
					if result.LastAssistantContent != content {
						t.Fatalf("content=%q want %q", result.LastAssistantContent, content)
					}
				}
				if calls != 0 || client.idx != 1 {
					t.Fatalf("prose completion invoked tools=%d provider calls=%d", calls, client.idx)
				}
			})
		}
	}
}

func proseCompletionCaseName(forced bool, content string) string {
	mode := "iteration cap"
	if forced {
		mode = "forced closeout"
	}
	if content == "" {
		return mode + "/tool only"
	}
	if content == " \n" {
		return mode + "/whitespace and tool"
	}
	return mode + "/prose and tool"
}
