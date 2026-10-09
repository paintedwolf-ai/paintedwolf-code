package session

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type sliceTurnError []string

func (sliceTurnError) Error() string { return "slice error" }

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
			mgr.llm = failingTurnClient{provider}
			sess, err := st.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create failure session", err)
			var delivered []error
			mgr.SetTurnFailureSink(func(_ context.Context, id string, cause error) {
				if id != sess.ID {
					t.Errorf("failure session = %s", id)
				}
				delivered = append(delivered, cause)
			})
			switch entry {
			case "prompt":
				_, err = mgr.Prompt(ctx, sess.ID, "Continue.")
			case "submission":
				in := PromptInput{Text: "Continue."}
				row, _, admitErr := mgr.AdmitPrompt(ctx, sess.ID, uuid.NewString(), in, in)
				testutil.FailErr(t, "admit failure submission", admitErr)
				_, err = mgr.RunPromptSubmission(ctx, row.ID)
			case "host":
				_, err = mgr.promptHostLoopWake(ctx, sess.ID)
			}
			if len(delivered) != 1 || !errors.Is(delivered[0], provider) || !errors.Is(err, provider) {
				t.Fatalf("delivery = %v, returned = %v; want one typed failure", delivered, err)
			}
			if UnreportedTurnFailure(err) != nil {
				t.Fatalf("caller would report the failure again: %v", err)
			}
		})
	}
}

func TestTurnFailureReportingPreservesUnreportedCauses(t *testing.T) {
	provider := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
	cleanup := errors.New("cleanup failed")
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	var delivered []error
	mgr := &Manager{}
	mgr.SetTurnFailureSink(func(ctx context.Context, id string, err error) {
		if ctx.Err() != nil || id != "session" {
			t.Errorf("delivery context/id = %v/%q", ctx.Err(), id)
		}
		delivered = append(delivered, err)
	})
	reported := mgr.reportTurnFailure(canceled, "session", provider)
	if len(delivered) != 1 || !errors.Is(delivered[0], provider) || !errors.Is(reported, provider) {
		t.Fatalf("reported failure lost its cause: delivered=%v returned=%v", delivered, reported)
	}
	cases := []struct {
		name string
		err  error
		want error
	}{
		{"nil", nil, nil},
		{"reported", reported, nil},
		{"wrapped report", fmt.Errorf("submission: %w", reported), nil},
		{"joined reports", errors.Join(reported, reported), nil},
		{"unreported", cleanup, cleanup},
		{"joined cleanup", errors.Join(reported, cleanup), cleanup},
		{"wrapped joined cleanup", fmt.Errorf("drain: %w", errors.Join(reported, cleanup)), cleanup},
		{"nested cleanup", errors.Join(reported, fmt.Errorf("persist: %w", cleanup)), cleanup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnreportedTurnFailure(tc.err)
			if !errors.Is(got, tc.want) {
				t.Fatalf("remaining error = %v, want %v", got, tc.want)
			}
			if got != nil && errors.Is(got, provider) {
				t.Fatal("reported provider failure would be published again")
			}
		})
	}
	if got := UnreportedTurnFailure(fmt.Errorf("uncomparable: %w", sliceTurnError{"failure"})); got == nil {
		t.Fatal("uncomparable error was discarded")
	}
	if got := (&Manager{}).reportTurnFailure(t.Context(), "session", provider); UnreportedTurnFailure(got) == nil {
		t.Fatal("missing sink marked failure as delivered")
	}
}

func TestFailedTurnBlocksAutomaticAdmissionButAllowsExplicitRetry(t *testing.T) {
	mgr, st := newTestManager(t)
	provider := &failure.ProviderEmptyCompletionError{ProviderID: "fixture"}
	mgr.llm = failingTurnClient{provider}
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	failures := 0
	mgr.SetTurnFailureSink(func(context.Context, string, error) { failures++ })
	for attempt := 1; attempt <= 2; attempt++ {
		_, err = mgr.Prompt(t.Context(), sess.ID, "Try again.")
		if !errors.Is(err, provider) || failures != attempt {
			t.Fatalf("explicit attempt %d: failures=%d error=%v", attempt, failures, err)
		}
		status, err := st.LatestTurnStatus(t.Context(), sess.ID)
		testutil.FailErr(t, "read failed turn", err)
		if status != store.TurnStatusFailed {
			t.Fatalf("turn status=%s", status)
		}
		loop := mgr.ensureCoordinatorRuntime().CoordinatorLoop()
		loop.Nudges.Nudge(t.Context(), sess.ID, anchor.PhaseAdvanced, "", "", anchor.Envelope{})
		if !loop.Nudges.HasPendingLoopWakes(sess.ID) {
			t.Fatal("failed turn discarded a new wake")
		}
		if !mgr.queueRoundComplete(t.Context(), sess.ID) {
			t.Fatal("held wake blocked new user input")
		}
		blockers, err := mgr.executionBlockers(t.Context(), store.ExecutionSession{ID: sess.ID, ProjectID: sess.ProjectID})
		testutil.FailErr(t, "observe settled failure", err)
		for _, blocker := range blockers {
			if blocker.Kind == "continuation" {
				t.Fatal("held wake reported active continuation")
			}
		}
		admitted := false
		_, err = mgr.runHostTurnWithAdmission(t.Context(), sess.ID, store.PromptSubmissionOriginLoopWake, PromptInput{}, func() error { admitted = true; return nil })
		testutil.FailErr(t, "held automatic turn", err)
		if admitted || failures != attempt {
			t.Fatalf("automatic retry: admitted=%v failures=%d", admitted, failures)
		}
	}
}
