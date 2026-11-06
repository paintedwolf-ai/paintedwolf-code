package wiring

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/promptresult"
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
			run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, api.StartWorkflowRunRequest{WorkflowID: "security-survey", WorkflowVersion: "2.0.1", Request: "Review the application's security boundaries."})
			testutil.FailErr(t, "start survey", err)
			settleScanObligationAndAdvance(t, h, ctx, run.ID, "plan")
			eventsCh, unsubscribe, err := h.Events.Subscribe(ctx, events.Subscription{Project: sess.ProjectID, Viewer: testutil.HostOwner()})
			testutil.FailErr(t, "subscribe to session settlement", err)
			t.Cleanup(unsubscribe)
			var resp *promptresult.Result
			if forced {
				resp, err = h.Sessions.Manager.Submissions.PromptHostTurn(ctx, sess.ID, store.PromptSubmissionOriginWorkerCloseout, "Finish the current turn.")
			} else {
				resp, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Continue the survey.")
			}
			testutil.FailErr(t, "settle stopped gated closeout as closeout_blocked", err)
			if resp == nil || !resp.IsCloseoutBlocked() || resp.Outcome != promptresult.OutcomeCloseoutBlocked || resp.CloseoutBlocked == nil {
				t.Fatalf("expected closeout_blocked outcome, got %+v", resp)
			}
			if resp.CloseoutBlocked.Code != "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT" ||
				resp.CloseoutBlocked.Phase != "plan" ||
				len(resp.CloseoutBlocked.OpenGateLeaves) == 0 ||
				!strings.Contains(resp.CloseoutBlocked.Notice, "Workflow phase plan is waiting on open gate(s):") {
				t.Fatalf("unexpected closeout_blocked payload: %+v", resp.CloseoutBlocked)
			}
			if n := calls.Load(); n == 0 || n > 8 {
				t.Fatalf("closeout calls=%d, want bounded model repair", n)
			}
			current, readErr := h.Workflows.Manager.Store.Runs.Get(ctx, run.ID)
			testutil.FailErr(t, "load retained workflow", readErr)
			if current.CurrentPhase != "plan" || current.Status == api.WorkflowRunStatusComplete || current.Status == api.WorkflowRunStatusFailed {
				t.Fatalf("closeout_blocked altered or failed workflow: %+v", current)
			}
			messages, readErr := h.Store.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "read transcript", readErr)
			var hostNoticeFound bool
			for _, msg := range messages {
				if msg.Role == api.MessageRoleAssistant && msg.Visibility == api.MessageVisibilityTranscript && msg.Content != "" {
					t.Fatalf("unsupported closeout published: %s", msg.Content)
				}
				if msg.Role == api.MessageRoleSystem &&
					msg.Origin == api.MessageOriginHost &&
					msg.Kind == api.MessageKindHostNudge &&
					msg.Visibility == api.MessageVisibilityTranscript &&
					msg.HostSignalID == "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT" &&
					msg.Content == resp.CloseoutBlocked.Notice {
					hostNoticeFound = true
				}
			}
			if !hostNoticeFound {
				t.Fatalf("missing host-written closeout_blocked notice in transcript: %+v", messages)
			}
			var sawGatePending, sawIdleCompleted bool
			for !sawGatePending || !sawIdleCompleted {
				select {
				case event := <-eventsCh:
					raw, marshalErr := json.Marshal(event.Data)
					testutil.FailErr(t, "read settlement event", marshalErr)
					if event.Topic == api.EventTopicWorkflow {
						var wfEvent api.WorkflowEvent
						if json.Unmarshal(raw, &wfEvent) == nil &&
							wfEvent.Event == api.WorkflowEventKindGatePending &&
							wfEvent.Phase == "plan" &&
							wfEvent.Status == string(promptresult.OutcomeCloseoutBlocked) &&
							wfEvent.BranchInstruction == resp.CloseoutBlocked.Notice {
							sawGatePending = true
						}
					} else if event.Topic == api.EventTopicSession {
						var status api.SessionEvent
						if json.Unmarshal(raw, &status) != nil || status.Status != api.SessionStatusIdle || status.IdleDisposition == "" {
							continue
						}
						if status.IdleDisposition != api.SessionIdleDispositionCompleted {
							t.Fatalf("idle disposition=%s, want completed", status.IdleDisposition)
						}
						if status.LastMessage != resp.CloseoutBlocked.Notice {
							t.Fatalf("idle session wire message=%q, want %q", status.LastMessage, resp.CloseoutBlocked.Notice)
						}
						sawIdleCompleted = true
					}
				case <-t.Context().Done():
					t.Fatalf("missing closeout_blocked wire events (gatePending=%t idleCompleted=%t)", sawGatePending, sawIdleCompleted)
				}
			}
			// Invariant: no session is left with an active workflow, idle workers,
			// and no pending ask, wake, or queued gate_blocked repair kick.
			pendingKicks := h.Sessions.Manager.Coordinator.Guidance.PendingIDs(ctx, sess.ID)
			if len(pendingKicks) == 0 {
				t.Fatal("expected pending gate_blocked kick while workflow remains active with idle workers")
			}

			// Next user prompt resumes repair at the same phase with the open gates in context.
			beforeResumeCalls := calls.Load()
			resumeResp, resumeErr := h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "Address the open gate requirements.")
			testutil.FailErr(t, "resume workflow after closeout_blocked", resumeErr)
			if resumeResp == nil || !resumeResp.IsCloseoutBlocked() || resumeResp.CloseoutBlocked.Phase != "plan" {
				t.Fatalf("resumed turn outcome=%+v", resumeResp)
			}
			if calls.Load() <= beforeResumeCalls {
				t.Fatal("resumed prompt made no model calls")
			}
			resumedMessages, readErr := h.Store.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "read resumed transcript", readErr)
			var sawOpenGateContext bool
			for _, msg := range resumedMessages {
				if strings.Contains(msg.Content, resp.CloseoutBlocked.OpenGateLeaves[0]) {
					sawOpenGateContext = true
					break
				}
			}
			if !sawOpenGateContext {
				t.Fatalf("resumed turn did not include open gate leaf %q in context", resp.CloseoutBlocked.OpenGateLeaves[0])
			}
		})
	}
}
