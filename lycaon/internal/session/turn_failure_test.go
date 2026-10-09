package session

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	sessionexecution "github.com/lycaon/lycaon/internal/session/execution"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type failingTurnClient struct{ failure error }

func (c failingTurnClient) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, c.failure
}

func (c failingTurnClient) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, c.failure
}

func TestFailedExecutionsReportOnceAndRetainTypedErrors(t *testing.T) {
	for _, entry := range []string{"prompt", "submission", "host"} {
		t.Run(entry, func(t *testing.T) {
			ctx := t.Context()
			mgr, st := newTestManager(t)
			provider := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
			mgr.Coordinator.Model.LLM = failingTurnClient{provider}
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create failure session", err)
			var delivered []error
			mgr.Runner.Turns.SetFailureSink(func(_ context.Context, id string, cause error) {
				if id != sess.ID {
					t.Errorf("failure session = %s", id)
				}
				delivered = append(delivered, cause)
			})
			switch entry {
			case "prompt":
				_, err = mgr.Submissions.Prompt(ctx, sess.ID, "Continue.")
			case "submission":
				in := promptinput.Input{Text: "Continue."}
				row, _, admitErr := mgr.Submissions.AdmitPrompt(ctx, sess.ID, uuid.NewString(), in, in)
				testutil.FailErr(t, "admit failure submission", admitErr)
				_, err = mgr.Submissions.RunPromptSubmission(ctx, row.ID)
			case "host":
				_, err = mgr.Submissions.LoopWake(ctx, sess.ID)
			}
			if len(delivered) != 1 || !errors.Is(delivered[0], provider) || !errors.Is(err, provider) {
				t.Fatalf("delivery = %v, returned = %v; want one typed failure", delivered, err)
			}
			if sessionexecution.UnreportedTurnFailure(err) != nil {
				t.Fatalf("caller would report the failure again: %v", err)
			}
		})
	}
}

func TestFailedTurnBlocksAutomaticAdmissionButAllowsExplicitRetry(t *testing.T) {
	mgr, st := newTestManager(t)
	provider := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
	mgr.Coordinator.Model.LLM = failingTurnClient{provider}
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	failures := 0
	mgr.Runner.Turns.SetFailureSink(func(context.Context, string, error) { failures++ })
	for attempt := 1; attempt <= 2; attempt++ {
		_, err = mgr.Submissions.Prompt(t.Context(), sess.ID, "Try again.")
		if !errors.Is(err, provider) || failures != attempt {
			t.Fatalf("explicit attempt %d: failures=%d error=%v", attempt, failures, err)
		}
		status, err := st.LatestTurnStatus(t.Context(), sess.ID)
		testutil.FailErr(t, "read failed turn", err)
		if status != store.TurnStatusFailed {
			t.Fatalf("turn status=%s", status)
		}
		loop := mgr.Coordinator.Runtime.CoordinatorLoop()
		loop.Nudge(t.Context(), sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		if !loop.HasPendingLoopWakes(sess.ID) {
			t.Fatal("failed turn discarded a new wake")
		}
		if !mgr.Admission.RoundComplete(t.Context(), sess.ID) {
			t.Fatal("held wake blocked new user input")
		}
		observed, err := mgr.Observations.Tree(t.Context(), sess.ID)
		testutil.FailErr(t, "observe settled failure", err)
		for _, blocker := range observed.Blockers {
			if blocker.Kind == "continuation" {
				t.Fatal("held wake reported active continuation")
			}
		}
		admitted := false
		_, err = mgr.Submissions.HostTurnWithAdmission(t.Context(), sess.ID, store.PromptSubmissionOriginLoopWake, promptinput.Input{}, func() error { admitted = true; return nil })
		testutil.FailErr(t, "held automatic turn", err)
		if admitted || failures != attempt {
			t.Fatalf("automatic retry: admitted=%v failures=%d", admitted, failures)
		}
	}
}
