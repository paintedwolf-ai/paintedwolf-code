package promptloop_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
	"github.com/lycaon/lycaon/pkg/api"
)

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
				ToolCtx: tools.ToolContext{SessionID: sess.ID, ParentSessionID: sess.ParentSessionID, Agent: sess.AgentType},
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
