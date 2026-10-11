package coordinator_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/assembly"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRuntimeRunPromptDelegatesToLoop(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}}})
	store := store.NewMemory()
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{
		LoopDeps: func() promptloop.PromptLoopDeps {
			return promptloop.PromptLoopDeps{
				Context: promptloop.ContextDeps{
					Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
					BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
						return history, nil
					},
				},
				Model: promptloop.ModelDeps{
					LLM: client,
				},
				Closeout: promptloop.CloseoutDeps{
					EvaluateCloseoutBlock: func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) { return nil, nil },
				},
				Projection: promptloop.ProjectionDeps{
					AppendMessages: store.AppendMessages,
					UpdateMessage: func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
						_, err := store.UpdateMessage(ctx, sessionID, messageID, msg)
						return err
					},
				},
			}
		},
	})
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	result, err := rt.RunPrompt(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "rt.RunPrompt failed", err)
	if result.LastAssistantContent != "ok" {
		t.Fatalf("content = %q want ok", result.LastAssistantContent)
	}
}

func TestRuntimeBeginEndPromptTurnAndDrainLoopPending(t *testing.T) {
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{
		AssemblyDeps: func() assembly.AssemblyDeps {
			return assembly.AssemblyDeps{}
		},
		LoopWakeDeps: func() loopwake.LoopDeps {
			return loopwake.LoopDeps{}
		},
	})
	rt.BeginPromptTurn("sess-1", "kick-1")
	rt.EndPromptTurn("sess-1")
	rt.DrainLoopPending(context.Background(), "sess-1")
	if rt.Kicks() == nil || rt.Board() == nil || rt.PromptLoop() == nil || rt.Assembly() == nil || rt.CoordinatorLoop() == nil {
		t.Fatal("expected runtime sub-engines")
	}
}

func TestRuntimeBuildCompletionMessagesPassthrough(t *testing.T) {
	rt := coordinator.NewRuntime(coordinator.RuntimeDeps{
		AssemblyDeps: func() assembly.AssemblyDeps {
			return assembly.AssemblyDeps{}
		},
	})
	history := []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}
	msgs, err := rt.BuildCompletionMessages(context.Background(), &api.Session{ID: "s1"}, history, nil)
	testutil.FailErr(t, "rt.BuildCompletionMessages failed", err)
	// With no assembly deps the runtime adds nothing of its own; the provenance
	// projection still prepends the one host content-authority system message,
	// so passthrough means history plus that notice and nothing else.
	if len(msgs) != 2 {
		t.Fatalf("msgs = %+v", msgs)
	}
	if msgs[0].Role != api.MessageRoleSystem || msgs[0].Content != transcript.ContentAuthorityNotice() {
		t.Fatalf("first message is not the content-authority notice: %+v", msgs[0])
	}
	if msgs[1].Role != api.MessageRoleUser || msgs[1].Content != "hi" {
		t.Fatalf("history not passed through: %+v", msgs[1])
	}
}
