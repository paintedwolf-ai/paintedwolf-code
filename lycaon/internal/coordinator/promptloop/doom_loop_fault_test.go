package promptloop

import (
	"context"
	"fmt"
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

func TestRepetitionContextUsesTypedProgressIndependentOfDisplay(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name, tool string
		advanced   *bool
		want       bool
	}{
		{"changed deliverable", "update_progress", &yes, true},
		{"bookkeeping", "update_progress", &no, false},
		{"missing fact", "update_progress", nil, false},
		{"write", "write", nil, true},
		{"read", "read", &yes, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contract, ok := toolcontract.Lookup(tc.tool)
			if !ok {
				t.Fatalf("missing contract for %s", tc.tool)
			}
			for _, body := range []string{"", `{"state_changed":true}`, "[progress#1]\nTruncated display\n>>> Tool feedback"} {
				run := completedToolRun{call: api.ToolCall{Name: tc.tool}, contract: contract, output: body, toolCtx: tools.ToolContext{Effects: tools.InvocationEffects{Out: &tools.ToolInvocationOut{Facts: guidance.ToolResultFacts{WorkflowAdvanced: tc.advanced}}}}}
				if got := run.advancesRepetitionContext(); got != tc.want {
					t.Fatalf("display changed advancement: %v want %v", got, tc.want)
				}
			}
		})
	}
}

func TestBatchProgressSeparatesNeutralResultsAndRefusals(t *testing.T) {
	outcome := batchProgressOutcome{}
	outcome.observe(false, &api.ToolResult{Tool: "update_progress", Outcome: api.ToolResultOutcomeCompleted, Content: `{"state_changed":true}`})
	if outcome.Advanced || outcome.Blocked {
		t.Fatalf("display overrode neutral fact: %+v", outcome)
	}
	outcome.observe(false, &api.ToolResult{Tool: "edit", Outcome: api.ToolResultOutcomeRejected})
	if outcome.Advanced || !outcome.Blocked {
		t.Fatalf("no-op masked rejected work: %+v", outcome)
	}
	outcome.observe(true, &api.ToolResult{Tool: "update_progress", Outcome: api.ToolResultOutcomeCompleted})
	if !outcome.Advanced {
		t.Fatal("stated advancement was lost")
	}
	empty := batchProgressOutcome{}
	empty.observe(true, nil)
	empty.observe(true, &api.ToolResult{Outcome: api.ToolResultOutcomeError})
	if empty.Advanced || !empty.Blocked {
		t.Fatalf("failed invocation advanced: %+v", empty)
	}
}

func TestBatchProgressConsumesInvocationFactsBeforeTranscriptProjection(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		tools                       []string
		advanced, refused, parallel bool
	}{
		{"neutral", []string{"update_progress"}, false, false, false},
		{"advancing", []string{"update_progress"}, true, false, false},
		{"neutral and rejected", []string{"update_progress", "edit"}, false, true, false},
		{"parallel neutral", []string{"read", "read"}, false, false, true},
		{"parallel advancing", []string{"read", "read"}, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewStubRegistry()
			for _, name := range []string{"update_progress", "read", "edit"} {
				testutil.FailErr(t, "register typed result producer", registry.Register(name, func(_ context.Context, _ map[string]any, call tools.ToolContext) (string, error) {
					advanced := tc.advanced
					call.Effects.Out.Facts.WorkflowAdvanced = &advanced
					return `{"state_changed":true}`, nil
				}))
			}
			loop := NewPromptLoopForTest(PromptLoopDeps{Context: ContextDeps{Tools: registry}, Tools: ToolsDeps{
				BeforeToolRun: func(_ context.Context, _ *api.Session, _ []api.Message, _ string, name string, _ map[string]any) (string, bool, error) {
					if tc.refused && name == "edit" {
						return "", false, guidance.NewRefusal("TOOL_ARGS_INVALID", "Rejected fixture")
					}
					return "", false, nil
				},
				EnrichToolOutput: func(_ context.Context, _ *api.Session, _ string, _ map[string]any, _ string, facts guidance.ToolResultFacts, _ int) (string, guidance.ToolResultFacts) {
					return "Display replaced after execution", facts
				},
			}, Projection: ProjectionDeps{AppendMessages: func(context.Context, string, ...api.Message) error { return nil }}})
			calls := make([]api.ToolCall, len(tc.tools))
			for index, name := range tc.tools {
				calls[index] = api.ToolCall{ID: fmt.Sprint(index), Name: name, Args: map[string]any{"path": fmt.Sprint(index), "content": "- [ ] Deliver"}}
			}
			if tc.parallel && len(partitionToolBatchRuns(registry, calls)[0]) != len(calls) {
				t.Fatal("fixture did not exercise parallel settlement")
			}
			session := &api.Session{ID: "session", Posture: api.SessionPostureBuild}
			history := []api.Message{{ID: "assistant", Role: api.MessageRoleAssistant, ToolCalls: calls}}
			batch, err := loop.Batch.executeToolCallsInTurn(t.Context(), session, session.ID, calls, tools.ToolContext{}, history, "work", "assistant", "", nil)
			testutil.FailErr(t, "execute typed batch", err)
			if batch.progress.Advanced != tc.advanced || batch.progress.Blocked != tc.refused {
				t.Fatalf("batch facts changed with display: %+v", batch.progress)
			}
			if len(batch.history) != len(calls)+1 {
				t.Fatalf("settled transcript lost calls: %d", len(batch.history))
			}
		})
	}
}
