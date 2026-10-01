package promptloop_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

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
				deps.LLM = client
				deps.Tools = registry
				deps.Policy = &recordingToolPolicy{}
				deps.Limits = loopTestLimits(1)
				deps.CoordinatorFrame = investigateCoordinatorContext()
				result, err := promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
					SessionID: sess.ID, Session: sess, History: userHistory("Read a.txt"),
					UserPrompt: "Read a.txt", ProfileID: "coordinator", ProseFinish: forced,
				})
				if content == "" || content == " \n" {
					if !errors.Is(err, failure.ErrProviderEmptyCompletion) || result != nil {
						t.Fatalf("empty prose result=%+v error=%v", result, err)
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
