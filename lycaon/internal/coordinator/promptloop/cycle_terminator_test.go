package promptloop

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExecuteToolCallsInTurnHostDecoratedCycleTerminator(t *testing.T) {
	tests := []struct {
		name       string
		tool       string
		toolOutput string
	}{
		{name: "worker decision", tool: "request_decision", toolOutput: `{"status":"decision_requested"}`},
		{name: "worker completion", tool: "complete_leg", toolOutput: `{"recorded":true}`},
		{name: "ask user", tool: "ask_user", toolOutput: `{"status":"pending","phase_id":"ask-1"}`},
		{name: "wait", tool: "wait", toolOutput: `{"status":"sleeping"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := tools.NewStubRegistry()
			err := reg.Register(tt.tool, func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
				if tt.tool == "wait" && tctx.Effects.Out != nil {
					tctx.Effects.Out.Completion = &api.ToolCompletion{Operation: "wait", State: "parked"}
				}
				return tt.toolOutput, nil
			})
			testutil.FailErr(t, "register cycle terminator", err)

			enricher := guidance.NewToolOutputEnricher(&guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{
				"SPEC_POSTURE_PROGRESS": {Message: "progress {{.progress}}"},
			}}, nil)
			loop := NewPromptLoopForTest(PromptLoopDeps{
				Tools: reg,
				AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
					return nil
				},
				EnrichToolOutput: func(_ context.Context, sess *api.Session, tool string, args map[string]any, output string, _ guidance.ToolResultFacts, _ int) (string, guidance.ToolResultFacts) {
					hostArgs := make(map[string]any, len(args)+1)
					for key, value := range args {
						hostArgs[key] = value
					}
					hostArgs["_pending_feedback_line"] = "pending_feedback: phase `ask-1`"
					enriched := enricher.Enrich(t.Context(), guidance.EnrichInput{
						SessionID: sess.ID,
						Session:   sess,
						Tool:      tool,
						Args:      hostArgs,
						Output:    output,
						PlanProgress: guidance.PlanProgress{
							PhaseInferred:     1,
							PhaseInferredName: "intake",
							NextAction:        "wait for input",
							ProgressChecklist: "[ ] intake",
							ChecklistHash:     "intake-1",
						},
					})
					return enriched.Output, enriched.Facts
				},
			})
			sess := &api.Session{ID: "sess-" + tt.tool, Posture: api.SessionPostureSpec}
			assistantID := "assistant-1"
			calls := []api.ToolCall{{ID: "call-1", Name: tt.tool, Args: map[string]any{}}}
			history := []api.Message{{ID: assistantID, Role: api.MessageRoleAssistant, ToolCalls: calls}}

			_, _, _, _, _, breakLoop, err := toolBatch{loop}.executeToolCallsInTurn(
				context.Background(), sess, sess.ID, calls, tools.ToolContext{
					Identity: tools.InvocationIdentity{SessionID: sess.ID},
				},
				history, "continue", assistantID, "", nil,
			)
			testutil.FailErr(t, "execute decorated cycle terminator", err)
			if !breakLoop {
				t.Fatalf("%s result did not end the tool cycle", tt.tool)
			}
		})
	}
}

func TestExecuteToolCallsInTurnHumanApprovalEndsCycle(t *testing.T) {
	reg := tools.NewStubRegistry()
	awaiting := false
	err := reg.Register("write", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		awaiting = true
		return `{"ok":true}`, nil
	})
	testutil.FailErr(t, "register write", err)

	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools: reg,
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
			return nil
		},
		HumanApprovalAwaiting: func(context.Context, string) bool { return awaiting },
	})
	sess := &api.Session{ID: "sess-hitl"}
	assistantID := "assistant-1"
	calls := []api.ToolCall{{ID: "call-1", Name: "write", Args: map[string]any{"path": "bp.md"}}}
	history := []api.Message{{ID: assistantID, Role: api.MessageRoleAssistant, ToolCalls: calls}}

	_, _, _, _, _, breakLoop, err := toolBatch{loop}.executeToolCallsInTurn(
		context.Background(), sess, sess.ID, calls, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
		history, "continue", assistantID, "", nil,
	)
	testutil.FailErr(t, "execute write while awaiting approval", err)
	if !breakLoop {
		t.Fatal("landing on human_approval must end the tool cycle")
	}
}

// A host-held phase ends a host-opened turn at its first tool boundary.
func TestExecuteToolCallsInTurnHostObligationEndsCycle(t *testing.T) {
	var held atomic.Bool
	held.Store(true)
	if !runHostHoldBatch(t, &held, "", func() {}) {
		t.Fatal("a host-held phase must end the tool cycle")
	}
}

// The person's turn keeps its tools under the hold, so a question asked while
// the host works ends in a reply rather than a bare tool call.
func TestExecuteToolCallsInTurnAnsweringUnderHostHoldContinues(t *testing.T) {
	var held atomic.Bool
	held.Store(true)
	var ran atomic.Bool
	if runHostHoldBatch(t, &held, toolcontract.SurfaceAwaitHost, func() { ran.Store(true) }) {
		t.Fatal("an await_host turn that started under the hold must continue to the reply")
	}
	if !ran.Load() {
		t.Fatal("the await_host surface must offer the status read")
	}
}

// A batch that moves the run into a host-held phase ends, whatever its surface.
func TestExecuteToolCallsInTurnEnteringHostHoldEndsCycle(t *testing.T) {
	var held atomic.Bool
	if !runHostHoldBatch(t, &held, toolcontract.SurfaceAwaitHost, func() { held.Store(true) }) {
		t.Fatal("entering a host-held phase must end the tool cycle")
	}
}

func runHostHoldBatch(t *testing.T, held *atomic.Bool, surfaceID string, onCall func()) bool {
	t.Helper()
	reg := tools.NewStubRegistry()
	err := reg.Register("scan_list", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		onCall()
		return `{"scans":[{"status":"pending"}]}`, nil
	})
	testutil.FailErr(t, "register scan_list", err)

	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools: reg,
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
			return nil
		},
		HumanApprovalAwaiting: func(context.Context, string) bool { return false },
		HostObligationHeld:    func(context.Context, string) bool { return held.Load() },
	})
	sess := &api.Session{ID: "sess-obligation"}
	assistantID := "assistant-1"
	calls := []api.ToolCall{{ID: "call-1", Name: "scan_list", Args: map[string]any{}}}
	history := []api.Message{{ID: assistantID, Role: api.MessageRoleAssistant, ToolCalls: calls}}
	var st *promptLoopTurnState
	if surfaceID != "" {
		plan, planErr := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: surfaceID}, 1)
		testutil.FailErr(t, "compile "+surfaceID, planErr)
		st = &promptLoopTurnState{turnToolPlan: plan}
	}

	_, _, _, _, _, breakLoop, err := toolBatch{loop}.executeToolCallsInTurn(
		context.Background(), sess, sess.ID, calls, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
		history, "continue", assistantID, surfaceID, st,
	)
	testutil.FailErr(t, "execute scan_list around a host hold", err)
	return breakLoop
}

func TestExecuteToolCallsInTurnWriteContinuesWhenNotAwaiting(t *testing.T) {
	reg := tools.NewStubRegistry()
	err := reg.Register("write", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return `{"ok":true}`, nil
	})
	testutil.FailErr(t, "register write", err)

	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools: reg,
		AppendMessages: func(_ context.Context, _ string, _ ...api.Message) error {
			return nil
		},
		HumanApprovalAwaiting: func(context.Context, string) bool { return false },
	})
	sess := &api.Session{ID: "sess-write"}
	assistantID := "assistant-1"
	calls := []api.ToolCall{{ID: "call-1", Name: "write", Args: map[string]any{"path": "bp.md"}}}
	history := []api.Message{{ID: assistantID, Role: api.MessageRoleAssistant, ToolCalls: calls}}

	_, _, _, _, _, breakLoop, err := toolBatch{loop}.executeToolCallsInTurn(
		context.Background(), sess, sess.ID, calls, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
		history, "continue", assistantID, "", nil,
	)
	testutil.FailErr(t, "execute write", err)
	if breakLoop {
		t.Fatal("write must not end the cycle when human approval is not awaiting")
	}
}

func TestWorkerDecisionSettlesUnattemptedCompletion(t *testing.T) {
	reg := tools.NewStubRegistry()
	testutil.FailErr(t, "register decision", reg.Register("request_decision", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return `{ "status": "decision_requested" }`, nil
	}))
	completed := false
	testutil.FailErr(t, "register completion", reg.Register("complete_leg", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		completed = true
		return `{ "recorded": true }`, nil
	}))
	loop := NewPromptLoopForTest(PromptLoopDeps{Tools: reg, AppendMessages: func(context.Context, string, ...api.Message) error { return nil }})
	calls := []api.ToolCall{{ID: "decision", Name: "request_decision"}, {ID: "completion", Name: "complete_leg"}}
	sess := &api.Session{ID: "worker", ParentSessionID: "parent"}
	history := []api.Message{{ID: "assistant", Role: api.MessageRoleAssistant, ToolCalls: calls}}
	history, _, _, _, _, stopped, err := toolBatch{loop}.executeToolCallsInTurn(t.Context(), sess, sess.ID, calls, tools.ToolContext{}, history, "implement", "assistant", "", nil)
	testutil.FailErr(t, "pause worker batch", err)
	if !stopped || completed || len(history) != 3 {
		t.Fatalf("stopped=%v completed=%v history=%+v", stopped, completed, history)
	}
	result := history[2].ToolResult
	if result == nil || result.ToolCallID != "completion" || result.Outcome != api.ToolResultOutcomeRejected || result.Invocation != nil || !strings.Contains(result.Content, "TOOL_BATCH_NOT_RUN") {
		t.Fatalf("unattempted result = %+v", result)
	}
}
