package wiring

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowCloseoutCannotPromiseUncommittedAdvancement(t *testing.T) {
	for _, forced := range []bool{false, true} {
		name := "ordinary"
		if forced {
			name = "forced"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			mock := llm.NewKickDrivenMock(llm.KickDrivenConfig{Fallback: func(context.Context, llm.Snapshot) modelcall.Completion {
				calls.Add(1)
				return modelcall.Completion{Content: "The gates passed. The host will advance the workflow."}
			}})
			h := BuildForTest(t, WithLLMClient(mock), WithoutCoordinatorLoop())
			ctx := h.OwnerCtx(t, t.Context())
			sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{Posture: api.SessionPostureVet}, h.ProjectDir(t, "incomplete-survey"))
			testutil.FailErr(t, "create session", err)
			run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "security-survey", WorkflowVersion: "2.0.0", Request: "Review the application's security boundaries."})
			testutil.FailErr(t, "start survey", err)
			settleScanObligationAndAdvance(t, h, ctx, run.ID, "plan")
			eventsCh, unsubscribe, err := h.Events.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
			testutil.FailErr(t, "subscribe to session settlement", err)
			t.Cleanup(unsubscribe)
			if forced {
				_, err = h.Sessions.Manager.Submissions.PromptHostTurn(ctx, sess.ID, store.PromptSubmissionOriginWorkerCloseout, "Finish the current turn.")
			} else {
				_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Continue the survey.")
			}
			if err == nil {
				t.Fatal("incomplete workflow accepted a successful closeout")
			}
			if n := calls.Load(); n == 0 || n > 8 {
				t.Fatalf("closeout calls=%d, want bounded model repair", n)
			}
			current, readErr := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
			testutil.FailErr(t, "load retained workflow", readErr)
			if current.CurrentPhase != "plan" || current.Status == api.WorkflowRunStatusComplete {
				t.Fatalf("unsupported completion changed workflow: %+v", current)
			}
			messages, readErr := h.Store.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "read transcript", readErr)
			for _, msg := range messages {
				if msg.Role == api.MessageRoleAssistant && msg.Visibility == api.MessageVisibilityTranscript && msg.Content != "" {
					t.Fatalf("unsupported closeout published: %s", msg.Content)
				}
			}
			deadline := time.After(2 * time.Second)
			for {
				select {
				case event := <-eventsCh:
					raw, marshalErr := json.Marshal(event.Data)
					testutil.FailErr(t, "read settlement event", marshalErr)
					var status api.SessionEvent
					if json.Unmarshal(raw, &status) != nil || status.Status != api.SessionStatusIdle || status.IdleDisposition == "" {
						continue
					}
					if status.IdleDisposition != api.SessionIdleDispositionTurnError {
						t.Fatalf("idle disposition=%s", status.IdleDisposition)
					}
					return
				case <-deadline:
					t.Fatal("missing failed turn settlement")
				}
			}
		})
	}
}
