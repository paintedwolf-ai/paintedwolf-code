package session

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptRecoveryPreservesIntentAndRejectsStaleActions(t *testing.T) {
	for _, posture := range []api.SessionPosture{api.SessionPostureBuild, api.SessionPostureVet} {
		t.Run(string(posture), func(t *testing.T) {
			mgr, st := newTestManager(t)
			ctx := t.Context()
			sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: posture}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create chat", err)
			original, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "review", PromptInput{Text: "Review the project"})
			testutil.FailErr(t, "admit original", err)
			claimed, _, err := st.ClaimPromptSubmission(ctx, original.ID)
			testutil.FailErr(t, "claim original", err)
			_, err = mgr.applyPromptUserTurn(ctx, sess.ID, PromptInput{Text: "Review the project", SubmissionID: original.ID})
			testutil.FailErr(t, "append original", err)
			progressID := uuid.NewString()
			testutil.FailErr(t, "append progress", st.AppendMessages(ctx, sess.ID, api.Message{ID: progressID, Role: api.MessageRoleAssistant, Content: "Survey completed", Visibility: api.MessageVisibilityTranscript}))
			testutil.FailErr(t, "fail provider", st.FinishPromptSubmission(ctx, original.ID, claimed.ClaimToken, store.PromptSubmissionFailed, "", store.PromptSubmissionFailure{Message: "provider disconnected"}))
			action := &api.PromptRecovery{Action: "continue", AfterMessageID: progressID}
			input := PromptInput{Text: "Keep going", Recovery: action}
			operation := uuid.NewString()
			resumed, created, err := mgr.AdmitPrompt(ctx, sess.ID, operation, input, input)
			testutil.FailErr(t, "admit recovery", err)
			if !created {
				t.Fatal("recovery not admitted")
			}
			var saved PromptInput
			testutil.FailErr(t, "decode durable recovery", json.Unmarshal([]byte(resumed.InputJSON), &saved))
			if !saved.Continuation || saved.Recovery == nil || saved.Recovery.AfterMessageID != action.AfterMessageID {
				t.Fatalf("recovery lost intent: %+v", saved)
			}
			replay, created, err := mgr.AdmitPrompt(ctx, sess.ID, operation, input, input)
			testutil.FailErr(t, "replay recovery", err)
			if created || replay.ID != resumed.ID {
				t.Fatal("replay created another recovery")
			}
			_, _, err = mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), input, input)
			if !errors.Is(err, ErrPromptRecoveryStale) {
				t.Fatalf("duplicate action = %v", err)
			}
			_, err = mgr.applyPromptUserTurn(ctx, sess.ID, saved)
			testutil.FailErr(t, "append continuation", err)
			history, err := st.GetMessages(ctx, sess.ID)
			testutil.FailErr(t, "read history", err)
			if api.UserTurnOrdinal(history) != 1 || history[len(history)-1].Kind != api.MessageKindUserContinuation {
				t.Fatalf("recovery opened another intent: %+v", history)
			}
		})
	}
}

func TestPromptRetryRestoresOriginalInputFromReceipt(t *testing.T) {
	mgr, st := newTestManager(t)
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create chat", err)
	originalInput := PromptInput{Text: "Implement the attached specification", ArtifactIDs: []string{uuid.NewString()}}
	original, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), originalInput, originalInput)
	testutil.FailErr(t, "admit original", err)
	claimed, _, err := st.ClaimPromptSubmission(ctx, original.ID)
	testutil.FailErr(t, "claim original", err)
	originalInput.SubmissionID = original.ID
	_, err = mgr.applyPromptUserTurn(ctx, sess.ID, originalInput)
	testutil.FailErr(t, "append original", err)
	testutil.FailErr(t, "interrupt original", st.FinishPromptSubmission(ctx, original.ID, claimed.ClaimToken, store.PromptSubmissionInterrupted, "", store.PromptSubmissionFailure{Message: "provider disconnected"}))
	restarted := NewManager(st, llm.NewMockProvider(testMockConfig(t)), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	input := PromptInput{Text: "client text is not the retry authority", Recovery: &api.PromptRecovery{Action: "retry", AfterMessageID: original.ID}}
	resumed, _, err := restarted.AdmitPrompt(ctx, sess.ID, uuid.NewString(), input, input)
	testutil.FailErr(t, "retry after manager restart", err)
	var saved PromptInput
	testutil.FailErr(t, "decode retry", json.Unmarshal([]byte(resumed.InputJSON), &saved))
	if saved.Text != originalInput.Text || !slices.Equal(saved.ArtifactIDs, originalInput.ArtifactIDs) || saved.Recovery == nil || saved.Recovery.AfterMessageID != original.ID || !saved.Continuation {
		t.Fatalf("retry did not restore original request: %+v", saved)
	}
}

func TestPromptRetryRejectsProgressAndChangedTranscript(t *testing.T) {
	mgr, st := newTestManager(t)
	ctx := t.Context()
	sess, err := st.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create chat", err)
	original, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), "implement", PromptInput{Text: "Implement"})
	testutil.FailErr(t, "admit original", err)
	claimed, _, err := st.ClaimPromptSubmission(ctx, original.ID)
	testutil.FailErr(t, "claim original", err)
	_, err = mgr.applyPromptUserTurn(ctx, sess.ID, PromptInput{Text: "Implement", SubmissionID: original.ID})
	testutil.FailErr(t, "append original", err)
	progress := api.Message{ID: uuid.NewString(), Role: api.MessageRoleAssistant, Content: "Work started", Visibility: api.MessageVisibilityTranscript}
	testutil.FailErr(t, "append progress", st.AppendMessages(ctx, sess.ID, progress))
	testutil.FailErr(t, "fail provider", st.FinishPromptSubmission(ctx, original.ID, claimed.ClaimToken, store.PromptSubmissionFailed, "", store.PromptSubmissionFailure{Message: "provider disconnected"}))
	for _, action := range []*api.PromptRecovery{{Action: "retry", AfterMessageID: progress.ID}, {Action: "continue", AfterMessageID: original.ID}} {
		input := PromptInput{Text: "Keep going", Recovery: action}
		_, _, err := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), input, input)
		if !errors.Is(err, ErrPromptRecoveryStale) {
			t.Fatalf("unsafe recovery %+v admitted: %v", action, err)
		}
	}
}

// Recovery changes routing; queue placement and visible text do not.
func TestPromptRoutingKeepsQueuedSlashCommands(t *testing.T) {
	for _, tc := range []struct {
		name      string
		input     PromptInput
		wantSlash bool
	}{
		{"new slash command", PromptInput{Text: "/security-survey"}, true},
		{"queued slash command", PromptInput{Text: "/security-survey", Continuation: true}, true},
		{"typed keep going", PromptInput{Text: "Keep going", Continuation: true}, true},
		{"continue recovery", PromptInput{Text: "Keep going", Continuation: true, Recovery: &api.PromptRecovery{Action: "continue"}}, false},
		{"retry original slash", PromptInput{Text: "/security-survey", Continuation: true, Recovery: &api.PromptRecovery{Action: "retry"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, st := newTestManager(t)
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create chat", err)
			view := &recoveryRoutingView{}
			workflowFixture1 := view
			mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1})
			_, err = mgr.runTurnLocked(t.Context(), sess.ID, tc.input)
			if !errors.Is(err, errRoutingObserved) {
				t.Fatalf("routing did not reach workflow boundary: %v", err)
			}
			if view.slash != tc.wantSlash {
				t.Fatalf("slash route = %v, want %v", view.slash, tc.wantSlash)
			}
		})
	}
}

var errRoutingObserved = errors.New("routing observed")

type recoveryRoutingView struct {
	recordingWorkflowView
	slash bool
}

func (v *recoveryRoutingView) TrySlashPrompt(context.Context, string, string, string) (*promptresult.Result, bool, error) {
	v.slash = true
	return nil, true, errRoutingObserved
}

func (v *recoveryRoutingView) AssertSessionRunnable(context.Context, string) error {
	return errRoutingObserved
}
