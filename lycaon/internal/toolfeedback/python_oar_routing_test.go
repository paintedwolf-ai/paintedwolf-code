package toolfeedback

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func pythonRecoveryFromRefusal(t *testing.T, err error, wantCode string) (*guidance.Refusal, pythonevidence.OARRecoveryFacts) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s refusal, got nil", wantCode)
	}
	refusal, ok := guidance.RefusalFromError(err)
	if !ok {
		t.Fatalf("expected guidance.Refusal, got %T: %v", err, err)
	}
	if refusal.Code() != wantCode {
		t.Fatalf("refusal code = %q, want %q", refusal.Code(), wantCode)
	}
	raw, exists := refusal.Facts.FeedbackFor(wantCode).Details[PythonOARRecoveryDetailKey]
	if !exists {
		t.Fatalf("missing %s in refusal details for %s", PythonOARRecoveryDetailKey, wantCode)
	}
	facts, ok := raw.(pythonevidence.OARRecoveryFacts)
	if !ok {
		t.Fatalf("unexpected %s type %T", PythonOARRecoveryDetailKey, raw)
	}
	return refusal, facts
}

// Trigger 1: A helper fails in direct Python with a normal exception.
// Also verifies successful tool exchanges return nil (no routing copy).
func TestOARTrigger1DirectPythonNormalExceptionAndSuccessFreeOfRoutingCopy(t *testing.T) {
	ctx := tools.WithCallReach(t.Context(), toolreach.Offered([]string{"python", "command"}))

	// Successful exchange must stay free of additional routing copy.
	if got := EvaluatePythonOrdinaryResult(ctx, "python", OrdinaryResultObservation{
		Succeeded:   true,
		RuntimeKind: pythonevidence.RuntimeKindPortablePython,
		ExitCode:    0,
	}); got != nil {
		t.Fatalf("successful tool exchange must not emit routing facts, got %+v", got)
	}

	// Normal exception in direct python preserves diagnostic & stays in python.
	facts := EvaluatePythonOrdinaryResult(ctx, "python", OrdinaryResultObservation{
		Succeeded:             false,
		RuntimeKind:           pythonevidence.RuntimeKindPortablePython,
		ChildOperation:        "script",
		StoppedOperationID:    "op-py-1",
		StoppedOperation:      "ValueError: invalid row",
		ExitCode:              1,
		AdmittedProjectRunner: "external -- python3 -m pytest",
	})
	if facts == nil {
		t.Fatal("expected OAR recovery facts for direct python exception")
	}
	if facts.InitiatingTool != "python" || facts.RuntimeKind != pythonevidence.RuntimeKindPortablePython {
		t.Fatalf("lost initiating tool or runtime kind: %+v", facts)
	}
	if !facts.DiagnosticPreserved || facts.ExitCode != 1 {
		t.Fatalf("diagnostic/exit status not preserved: %+v", facts)
	}
	if facts.ReachableDirectPython == nil || facts.ReachableDirectPython.Tool != "python" {
		t.Fatalf("expected direct python correction route, got %+v", facts)
	}
	if facts.AdmittedExternalRunner != nil {
		t.Fatalf("direct python exception must never suggest command/external interpreter: %+v", facts.AdmittedExternalRunner)
	}
}

// Trigger 2: Command reaches a bare interpreter for an agent helper (PYTHON_TOOL_REQUIRED).
// Rendered through the real stock OAR pack across offered, loadable, and absent surfaces.
func TestOARTrigger2CommandBareInterpreterRoutesThroughStockPack(t *testing.T) {
	bp := stockBlockPlane(t)

	cases := []struct {
		name           string
		reach          toolreach.Request
		wantDirect     bool
		wantRequest    bool
		wantNextTools  []string
		wantTextSubstr string
	}{
		{
			name:           "python offered",
			reach:          toolreach.Offered([]string{"command", "python"}),
			wantDirect:     true,
			wantNextTools:  []string{"python"},
			wantTextSubstr: "`python(",
		},
		{
			name: "python loadable via request_tools",
			reach: toolreach.Freeze(
				[]string{"command", "request_tools"},
				toolsurface.Compile([]string{"command", "request_tools"}, []string{"python"}),
			),
			wantRequest:    true,
			wantNextTools:  []string{"request_tools", "python"},
			wantTextSubstr: `request_tools(need="python")`,
		},
		{
			name:          "python absent",
			reach:         toolreach.Offered([]string{"command"}),
			wantNextTools: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tools.WithCallReach(t.Context(), tc.reach)
			err := bp.RejectObservation(ctx, "command", "implement", map[string]any{"command": "echo ok && python3 helper.py"}, &toolrejection.ToolReject{
				Code: "PYTHON_TOOL_REQUIRED",
				Data: map[string]any{
					"command":                 "python3",
					"source_line":             1,
					"source_column":           12,
					"stage_name":              "python3",
					"stopped_operation_id":    "op-stage-2",
					"prior_completed_effects": 1,
				},
			})

			refusal, facts := pythonRecoveryFromRefusal(t, err, "PYTHON_TOOL_REQUIRED")
			if facts.InitiatingTool != "command" {
				t.Fatalf("InitiatingTool = %q, want command", facts.InitiatingTool)
			}
			if facts.StoppedOperationID != "op-stage-2" || facts.StoppedOperation != "python3" || facts.PriorEffects != 1 {
				t.Fatalf("stopped stage or prior effects lost: %+v", facts)
			}
			if (facts.ReachableDirectPython != nil) != tc.wantDirect {
				t.Fatalf("ReachableDirectPython = %+v, want present=%v", facts.ReachableDirectPython, tc.wantDirect)
			}
			if (facts.RequestableNeed != nil) != tc.wantRequest {
				t.Fatalf("RequestableNeed = %+v, want present=%v", facts.RequestableNeed, tc.wantRequest)
			}

			// Verify real OAR pack rendered copy and next_calls match reachability.
			calls, _ := refusal.Facts.FeedbackFor("PYTHON_TOOL_REQUIRED").Details[api.ToolFeedbackNextCalls].([]toolreach.Call)
			if len(calls) != len(tc.wantNextTools) {
				t.Fatalf("rendered next_calls = %+v, want tools %v", calls, tc.wantNextTools)
			}
			for i, wantTool := range tc.wantNextTools {
				if calls[i].Tool != wantTool {
					t.Fatalf("next_calls[%d].Tool = %q, want %q", i, calls[i].Tool, wantTool)
				}
			}
			if tc.wantTextSubstr != "" && !strings.Contains(refusal.Copy["fix"], tc.wantTextSubstr) {
				t.Fatalf("rendered fix %q missing %q", refusal.Copy["fix"], tc.wantTextSubstr)
			}
			if !strings.Contains(refusal.Copy["what"], "1:12") {
				t.Fatalf("rendered what %q did not preserve stopped stage location 1:12", refusal.Copy["what"])
			}
		})
	}
}

// Trigger 3: A declared project runner reports a failing test or missing project dependency.
func TestOARTrigger3DeclaredProjectRunnerFailureNeverSuggestsPortablePython(t *testing.T) {
	ctx := tools.WithCallReach(t.Context(), toolreach.Offered([]string{"command", "python", "request_tools"}))
	facts := EvaluatePythonOrdinaryResult(ctx, "command", OrdinaryResultObservation{
		Succeeded:             false,
		RuntimeKind:           pythonevidence.RuntimeKindProjectRunner,
		StageName:             "pytest",
		ChildOperation:        "external_process",
		StoppedOperationID:    "op-pytest-1",
		StoppedOperation:      "external -- python3 -m pytest",
		ExitCode:              1,
		AdmittedProjectRunner: "external -- python3 -m pytest",
	})
	if facts == nil {
		t.Fatal("expected OAR recovery facts for project runner failure")
	}
	if facts.InitiatingTool != "command" || facts.RuntimeKind != pythonevidence.RuntimeKindProjectRunner {
		t.Fatalf("lost project runner identity: %+v", facts)
	}
	if !facts.DiagnosticPreserved || facts.ExitCode != 1 {
		t.Fatalf("expected preserved diagnostic and exit code 1, got %+v", facts)
	}
	if facts.ReachableDirectPython != nil || facts.RequestableNeed != nil {
		t.Fatalf("project runner failure must NEVER suggest portable python: %+v", facts)
	}
	if facts.AdmittedExternalRunner == nil || facts.AdmittedExternalRunner.Command != "external -- python3 -m pytest" {
		t.Fatalf("expected preserved admitted project runner, got %+v", facts.AdmittedExternalRunner)
	}
}

// Trigger 4: A command pipeline fails in a shell stage even when a portable Python stage exists elsewhere.
func TestOARTrigger4CommandPipelineShellStageFailureKeepsCommandInitiator(t *testing.T) {
	ctx := tools.WithCallReach(t.Context(), toolreach.Offered([]string{"command", "python"}))
	facts := EvaluatePythonOrdinaryResult(ctx, "command", OrdinaryResultObservation{
		Succeeded:             false,
		RuntimeKind:           pythonevidence.RuntimeKindShellStage,
		StageName:             "awk",
		ChildOperation:        "pipeline_stage",
		StoppedOperationID:    "op-stage-awk-3",
		StoppedOperation:      "awk '{print $1}' missing.csv",
		PriorCompletedEffects: 2, // earlier portable Python stage completed
		ExitCode:              2,
	})
	if facts == nil {
		t.Fatal("expected OAR recovery facts for shell stage failure")
	}
	if facts.InitiatingTool != "command" || facts.RuntimeKind != pythonevidence.RuntimeKindShellStage {
		t.Fatalf("pipeline shell failure must preserve command initiator and shell_stage runtime: %+v", facts)
	}
	if facts.StageName != "awk" || facts.StoppedOperationID != "op-stage-awk-3" || facts.PriorEffects != 2 {
		t.Fatalf("stage identity or prior effects lost: %+v", facts)
	}
	if facts.ReachableDirectPython != nil || facts.RequestableNeed != nil {
		t.Fatalf("shell stage failure must not route to python: %+v", facts)
	}
}

// Trigger 5: Either tool (python or command) reaches the same denied HTTP request or workspace write.
func TestOARTrigger5DeniedHTTPOrWriteForbidsLanguageSwitchAndPointsToHeldOp(t *testing.T) {
	bp := stockBlockPlane(t)
	ctx := tools.WithCallReach(t.Context(), toolreach.Offered([]string{"python", "command", "read"}))

	for _, toolName := range []string{"python", "command"} {
		t.Run(toolName+"_http_denied", func(t *testing.T) {
			err := bp.RejectObservation(ctx, toolName, "implement", nil, &toolrejection.ToolReject{
				Code: "HTTP_REQUEST_HOST_DENIED",
				Data: map[string]any{
					"rejection_reason":        "egress destination not approved",
					"http_requests_started":   1,
					"http_responses_received": 0,
					"child_operation":         "http_post",
					"runtime_kind":            pythonevidence.RuntimeKindHTTP,
					"held_operation_id":       "hold-http-42",
				},
			})
			_, facts := pythonRecoveryFromRefusal(t, err, "HTTP_REQUEST_HOST_DENIED")
			if facts.InitiatingTool != toolName {
				t.Fatalf("InitiatingTool = %q, want %q", facts.InitiatingTool, toolName)
			}
			if facts.ReachableDirectPython != nil || facts.RequestableNeed != nil || facts.AdmittedExternalRunner != nil {
				t.Fatalf("HTTP denial must never suggest switching languages: %+v", facts)
			}
			if facts.PendingHeldOperationID != "hold-http-42" || facts.NoRecoveryRoute {
				t.Fatalf("expected pending held operation hold-http-42 as recovery path, got %+v", facts)
			}
		})

		t.Run(toolName+"_workspace_write_denied", func(t *testing.T) {
			err := bp.RejectObservation(ctx, toolName, "worker_readonly", nil, &toolrejection.ToolReject{
				Code: "COMMAND_EFFECT_NOT_ALLOWED",
				Data: map[string]any{
					"expected_form":         "read_only",
					"path":                  "src/main.go",
					"child_operation":       "workspace_write",
					"runtime_kind":          pythonevidence.RuntimeKindWorkspaceWrite,
					"admitted_project_runner": "external -- python3",
				},
			})
			_, facts := pythonRecoveryFromRefusal(t, err, "COMMAND_EFFECT_NOT_ALLOWED")
			if facts.InitiatingTool != toolName {
				t.Fatalf("InitiatingTool = %q, want %q", facts.InitiatingTool, toolName)
			}
			if facts.ReachableDirectPython != nil || facts.AdmittedExternalRunner != nil {
				t.Fatalf("workspace write denial must never suggest switching languages or external execution: %+v", facts)
			}
		})
	}
}

// Trigger 6: A tool becomes loadable or absent, or the surface changes after a hold.
func TestOARTrigger6SurfaceChangeAfterHoldPreservesOccurrenceAndRecomputesReach(t *testing.T) {
	bp := stockBlockPlane(t)
	reject := &toolrejection.ToolReject{
		Code: "PYTHON_TOOL_REQUIRED",
		Data: map[string]any{
			"command":                 "python3",
			"source_line":             3,
			"source_column":           1,
			"stopped_operation_id":    "op-held-then-changed",
			"prior_completed_effects": 2,
		},
	}

	// Surface A: loader offered AND python deferred -> offers request_tools(need="python").
	ctxLoadable := tools.WithCallReach(t.Context(), toolreach.Freeze(
		[]string{"command", "request_tools"},
		toolsurface.Compile([]string{"command", "request_tools"}, []string{"python"}),
	))
	refLoadable, factsLoadable := pythonRecoveryFromRefusal(t, bp.RejectObservation(ctxLoadable, "command", "implement", nil, reject), "PYTHON_TOOL_REQUIRED")
	if factsLoadable.InitiatingTool != "command" || factsLoadable.StoppedOperationID != "op-held-then-changed" || factsLoadable.PriorEffects != 2 {
		t.Fatalf("original occurrence lost: %+v", factsLoadable)
	}
	if factsLoadable.RequestableNeed == nil || factsLoadable.RequestableNeed.Need != "python" {
		t.Fatalf("expected request_tools(need=\"python\") when loadable, got %+v", factsLoadable)
	}
	if !strings.Contains(refLoadable.Copy["fix"], `request_tools(need="python")`) {
		t.Fatalf("rendered copy missing loader step: %s", refLoadable.Copy["fix"])
	}

	// Surface B: python deferred, but loader (`request_tools`) NOT offered -> MUST omit loader!
	ctxNoLoader := tools.WithCallReach(t.Context(), toolreach.Freeze(
		[]string{"command"},
		toolsurface.Compile([]string{"command"}, []string{"python"}),
	))
	refNoLoader, factsNoLoader := pythonRecoveryFromRefusal(t, bp.RejectObservation(ctxNoLoader, "command", "implement", nil, reject), "PYTHON_TOOL_REQUIRED")
	if factsNoLoader.RequestableNeed != nil || factsNoLoader.ReachableDirectPython != nil || !factsNoLoader.NoRecoveryRoute {
		t.Fatalf("must omit loader when request_tools is not offered: %+v", factsNoLoader)
	}
	if calls, _ := refNoLoader.Facts.FeedbackFor("PYTHON_TOOL_REQUIRED").Details[api.ToolFeedbackNextCalls].([]toolreach.Call); len(calls) != 0 {
		t.Fatalf("expected zero next_calls when loader unavailable, got %+v", calls)
	}
}
