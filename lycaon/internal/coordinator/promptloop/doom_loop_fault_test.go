package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordingDoomLoop captures what the repetition guard was charged for.
type recordingDoomLoop struct {
	surveyOutcomeRecorder
	codes []string
}

func (g *recordingDoomLoop) Check(context.Context, string, string, string, map[string]any, bool, *tools.AgentReadBases) (bool, int, string, error) {
	return true, 0, "", nil
}

func (g *recordingDoomLoop) RecordAttempt(_ context.Context, _, _, _ string, _ map[string]any, failure *api.InvocationFailure, _ bool) error {
	code := ""
	if failure != nil {
		code = failure.Code
	}
	g.codes = append(g.codes, code)
	return nil
}

func settleFailure(t *testing.T, guard *recordingDoomLoop, failure *api.InvocationFailure, code string) singleToolOutcome {
	t.Helper()
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Nudges: NudgesDeps{
			DoomLoop: guard,
		},
		Projection: ProjectionDeps{
			AppendMessages: func(context.Context, string, ...api.Message) error { return nil },
		},
	})
	run := toolInvocation{
		content: "Rejected: something\nCode: " + code,
		facts:   guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeError}.WithCode(code),
		failure: failure,
	}
	return loop.Batch.settleRejectedToolCall(
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
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Tools: registry,
		},
	})
	run := loop.Tools.executeToolCall(t.Context(), &api.Session{ID: "s1"}, "s1", "", nil, api.ToolCall{
		ID: "call-1", Name: "read", Args: map[string]any{},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s1"},
	}, nil, 0, "", api.CoordinatorRunContext{})
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

func TestRepetitionContextRequiresMaterialProgress(t *testing.T) {
	for _, tc := range []struct {
		name, tool, output string
		want               bool
	}{
		{"document only", "update_progress", `{"status":"updated","state_changed":false}`, false},
		{"unchanged", "update_progress", `{"status":"unchanged"}`, false},
		{"state changed", "update_progress", `{"status":"updated","state_changed":true}`, true},
		{"acknowledged", "update_progress", `{"status":"unchanged","reconciliation_status":"acknowledged"}`, true},
		{"unknown", "update_progress", `{"status":"unknown"}`, false},
		{"missing", "update_progress", `{}`, false},
		{"invalid", "update_progress", `updated`, false},
		{"write", "write", `ok`, true},
		{"read", "read", `ok`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contract, ok := toolcontract.Lookup(tc.tool)
			if !ok {
				t.Fatalf("missing contract for %s", tc.tool)
			}
			run := completedToolRun{call: api.ToolCall{Name: tc.tool}, contract: contract, output: tc.output}
			if got := run.advancesRepetitionContext(); got != tc.want {
				t.Fatalf("repetition context advance = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestChecklistNoOpAndRelabelDoNotAdvanceBatch(t *testing.T) {
	for _, body := range []string{`{"status":"unchanged","state_changed":false}`, `{"status":"updated","document_changed":true,"state_changed":false}`} {
		history := []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "update_progress", Content: body, Outcome: api.ToolResultOutcomeCompleted}}}
		if result := classifyBatchProgress(history, "assistant"); result.Advanced || result.Blocked {
			t.Fatalf("bookkeeping advanced batch: %s", body)
		}
		history[0].ToolResult.Content = `{"status":"updated","state_changed":true,"reconciled":true}`
		if !classifyBatchProgress(history, "assistant").Advanced {
			t.Fatal("deliverable transition did not advance batch")
		}
		history = append(history, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "command", Outcome: api.ToolResultOutcomeCompleted}})
		if !classifyBatchProgress(history, "assistant").Advanced {
			t.Fatal("checklist prevented independent work")
		}
	}
}

func TestNeutralChecklistCannotHideARejectedOperation(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "update_progress", Content: `{"status":"unchanged"}`, Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "edit", Outcome: api.ToolResultOutcomeRejected}},
	}
	result := classifyBatchProgress(history, "assistant")
	if result.Advanced || !result.Blocked {
		t.Fatalf("no-op masked rejected work: %+v", result)
	}
}

func TestProgressClassificationPreservesDeclaredResultFraming(t *testing.T) {
	for _, body := range []string{`{"status":"unchanged","state_changed":false}`, `{"status":"updated","state_changed":true}`, `{"status":"unchanged","reconciliation_status":"acknowledged"}`} {
		for _, decoration := range []struct{ prefix, suffix string }{{"", ""}, {"[progress#1]\n", "\n\n>>> Tool feedback\nRecorded diagnostic"}} {
			plain := []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "update_progress", Content: body, Outcome: api.ToolResultOutcomeCompleted}}}
			expected := classifyBatchProgress(plain, "assistant")
			plain[0].ToolResult.Content = decoration.prefix + body + decoration.suffix
			if actual := classifyBatchProgress(plain, "assistant"); actual != expected {
				t.Fatalf("declared framing changed outcome: plain=%+v decorated=%+v", expected, actual)
			}
			if progressResultAdvanced(decoration.prefix+body+decoration.suffix) != expected.Advanced {
				t.Fatal("raw and transcript progress outcomes diverged")
			}
		}
	}
}
