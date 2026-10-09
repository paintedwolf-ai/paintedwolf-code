package promptloop

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordingDoomLoop captures what the repetition guard was charged for.
type recordingDoomLoop struct {
	surveyOutcomeRecorder
	codes []string
}

func (g *recordingDoomLoop) Check(context.Context, string, string, string, map[string]any) (bool, int, string, error) {
	return true, 0, "", nil
}

func (g *recordingDoomLoop) RecordAttempt(_ context.Context, _, _, _ string, _ map[string]any, rejectCode string, _ bool) error {
	g.codes = append(g.codes, rejectCode)
	return nil
}

func settleFailure(t *testing.T, guard *recordingDoomLoop, failure *api.InvocationFailure, code string) singleToolOutcome {
	t.Helper()
	loop := NewPromptLoopForTest(PromptLoopDeps{
		DoomLoop:       guard,
		AppendMessages: func(context.Context, string, ...api.Message) error { return nil },
	})
	run := toolInvocation{
		content: "Rejected: something\nCode: " + code,
		facts:   guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeError}.WithCode(code),
		failure: failure,
	}
	return toolBatch{loop}.settleRejectedToolCall(
		context.Background(), &api.Session{ID: "s1"}, "s1",
		api.ToolCall{ID: "call-1", Name: "task"}, "a1", run,
	)
}

// An unsettled subsystem owner does not charge the caller's retry budget.
func TestOwnerFailureIsNotChargedToTheCaller(t *testing.T) {
	guard := &recordingDoomLoop{}
	settleFailure(t, guard, &api.InvocationFailure{
		Code:  toolrejection.ToolOwnerFailedCode,
		Class: api.FailureClassOwnerError,
	}, toolrejection.ToolOwnerFailedCode)

	if len(guard.codes) != 0 {
		t.Errorf("recorded %v against the caller, want nothing for a host-managed failure", guard.codes)
	}
}

func TestCallerRejectionIsStillCharged(t *testing.T) {
	guard := &recordingDoomLoop{}
	settleFailure(t, guard, &api.InvocationFailure{
		Code:  "PROGRESS_MISSING",
		Class: api.FailureClassHostRejection,
	}, "PROGRESS_MISSING")

	if len(guard.codes) != 1 || guard.codes[0] != "PROGRESS_MISSING" {
		t.Errorf("recorded %v, want one PROGRESS_MISSING", guard.codes)
	}
}

func TestRenderedOwnerFailureKeepsOwnershipClassification(t *testing.T) {
	rendered := guidance.NewRefusal("HTTP_REQUEST_FAILED", "request failed").WithCause(&toolrejection.ToolReject{
		Code: "HTTP_TRANSPORT_ENDED", FailureClass: api.FailureClassOwnerError, Retryable: true,
		Data: map[string]any{"reason": "remote ended the response"},
	})
	registry := tools.NewStubRegistry()
	testutil.FailErr(t, "register read", registry.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "", rendered
	}))
	loop := NewPromptLoopForTest(PromptLoopDeps{Tools: registry})
	run := toolInvocations{loop}.executeToolCall(t.Context(), &api.Session{ID: "s1"}, "s1", "", nil, api.ToolCall{
		ID: "call-1", Name: "read", Args: map[string]any{},
	}, tools.ToolContext{SessionID: "s1"}, nil, 0, "", api.CoordinatorRunContext{})
	failure := run.failure
	if failure == nil {
		t.Fatal("rendered tool rejection has no invocation failure")
	}
	if failure.Class != api.FailureClassOwnerError || !failure.Retryable || failure.Code != "HTTP_REQUEST_FAILED" {
		t.Fatalf("failure = %+v", failure)
	}

	guard := &recordingDoomLoop{}
	settleFailure(t, guard, failure, failure.Code)
	if len(guard.codes) != 0 {
		t.Errorf("recorded %v against the caller, want nothing for a rendered owner failure", guard.codes)
	}
}

// A rejection with no typed failure is a plain rejection and keeps counting.
func TestUntypedRejectionKeepsCounting(t *testing.T) {
	guard := &recordingDoomLoop{}
	settleFailure(t, guard, nil, "SOME_CODE")
	if len(guard.codes) != 1 {
		t.Errorf("recorded %v, want the rejection counted", guard.codes)
	}
}

func TestCallerFaultClassification(t *testing.T) {
	cases := map[string]struct {
		failure *api.InvocationFailure
		want    bool
	}{
		"nil":              {nil, true},
		"owner error":      {&api.InvocationFailure{Class: api.FailureClassOwnerError}, false},
		"host rejection":   {&api.InvocationFailure{Class: api.FailureClassHostRejection}, true},
		"policy rejection": {&api.InvocationFailure{Class: api.FailureClassPolicyRejection}, true},
	}
	for name, c := range cases {
		if got := c.failure.CallerFault(); got != c.want {
			t.Errorf("%s: CallerFault() = %v, want %v", name, got, c.want)
		}
	}
}

func (*recordingDoomLoop) ResolveRejection(context.Context, string, string, map[string]any, string) error {
	return nil
}
