package toolfeedback

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func requireRefusal(t *testing.T, err error, wantCode string) (*guidance.Refusal, []toolreach.Call) {
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
	fb := refusal.Facts.FeedbackFor(wantCode)
	// Never serialize a duplicate routing description in details.
	if _, dup := fb.Details["python_oar_recovery"]; dup {
		t.Fatalf("refusal details must not serialize duplicate python_oar_recovery object")
	}
	calls, _ := fb.Details[api.ToolFeedbackNextCalls].([]toolreach.Call)
	return refusal, calls
}

// Trigger 1: A helper fails in direct Python with a normal exception (and
// ordinary direct Python calls succeed without any extra OAR routing copy).
func TestOARTrigger1DirectPythonNoExtraRoutingCopy(t *testing.T) {
	bp := stockBlockPlane(t)
	ctx := tools.WithCallReach(t.Context(), toolreach.Offered([]string{"python", "command"}))

	// Normal pre-invoke of direct python produces no block or routing copy.
	if err := bp.Evaluate(ctx, oar.AnchorToolPreInvoke, "python", "implement", map[string]any{"code": "raise ValueError('bad row')"}, nil); err != nil {
		t.Fatalf("ordinary python invocation must not trigger OAR block/routing: %v", err)
	}

	// Ordinary non-policy exception from python is not rewritten into a command/external suggestion.
	err := bp.RejectObservation(ctx, "python", "implement", map[string]any{"code": "raise ValueError('bad row')"}, &toolrejection.ToolReject{
		Code: "PYTHON_RUNTIME_EXCEPTION",
		Data: map[string]any{"diagnostic": "ValueError: bad row"},
	})
	if err != nil {
		if refusal, ok := guidance.RefusalFromError(err); ok {
			if strings.Contains(refusal.Error(), "`command(") || strings.Contains(refusal.Error(), "external --") {
				t.Fatalf("direct python exception must not suggest command or external interpreter: %s", refusal.Error())
			}
		}
	}
}

// Trigger 2: Command reaches a bare interpreter for an agent helper (PYTHON_TOOL_REQUIRED).
// Verifies actual rendered OAR text and next_calls across offered, loadable, and absent surfaces.
func TestOARTrigger2CommandBareInterpreterRenderedPackRouting(t *testing.T) {
	bp := stockBlockPlane(t)

	cases := []struct {
		name           string
		reach          toolreach.Request
		wantNextTools  []string
		wantNeedArg    string
		wantFixSubstr  string
		forbidSubstrs  []string
	}{
		{
			name:          "python offered",
			reach:         toolreach.Offered([]string{"command", "python"}),
			wantNextTools: []string{"python"},
			wantFixSubstr: "`python(…)` using `code`, `script`, or `module`",
			forbidSubstrs: []string{"request_tools"},
		},
		{
			name: "python loadable via request_tools",
			reach: toolreach.Freeze(
				[]string{"command", "request_tools"},
				toolsurface.Compile([]string{"command", "request_tools"}, []string{"python"}),
			),
			wantNextTools: []string{"request_tools", "python"},
			wantNeedArg:   "python",
			wantFixSubstr: `after loading it with ` + "`request_tools(need=\"python\")`",
		},
		{
			name:          "python absent",
			reach:         toolreach.Offered([]string{"command"}),
			wantNextTools: nil,
			forbidSubstrs: []string{"`python(", "request_tools"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tools.WithCallReach(t.Context(), tc.reach)
			err := bp.RejectObservation(ctx, "command", "implement", map[string]any{"command": "echo ok && python3 helper.py"}, &toolrejection.ToolReject{
				Code: "PYTHON_TOOL_REQUIRED",
				Data: map[string]any{
					"stopped_operation":       "python3",
					"source_line":             1,
					"source_column":           12,
					"prior_completed_effects": 1,
				},
			})

			refusal, calls := requireRefusal(t, err, "PYTHON_TOOL_REQUIRED")
			if len(calls) != len(tc.wantNextTools) {
				t.Fatalf("next_calls = %+v, want %v", calls, tc.wantNextTools)
			}
			for i, wantTool := range tc.wantNextTools {
				if calls[i].Tool != wantTool {
					t.Fatalf("next_calls[%d].Tool = %q, want %q", i, calls[i].Tool, wantTool)
				}
			}
			if tc.wantNeedArg != "" && calls[0].Args["need"] != tc.wantNeedArg {
				t.Fatalf("request_tools need arg = %v, want %q", calls[0].Args["need"], tc.wantNeedArg)
			}
			// Pack consumed `command` published before rendering into `what`.
			if !strings.Contains(refusal.Copy["what"], "Bare python3 launch at 1:12 stopped before startup.") {
				t.Fatalf("rendered what did not consume stopped stage facts: %q", refusal.Copy["what"])
			}
			if !strings.Contains(refusal.Copy["why"], "Earlier completed operations remain authoritative") {
				t.Fatalf("rendered why lost prior-effects preservation: %q", refusal.Copy["why"])
			}
			if tc.wantFixSubstr != "" && !strings.Contains(refusal.Copy["fix"], tc.wantFixSubstr) {
				t.Fatalf("rendered fix %q missing %q", refusal.Copy["fix"], tc.wantFixSubstr)
			}
			for _, bad := range tc.forbidSubstrs {
				if strings.Contains(refusal.Copy["fix"], bad) {
					t.Fatalf("rendered fix %q unexpectedly mentions %q", refusal.Copy["fix"], bad)
				}
			}
		})
	}
}

// Trigger 3: A declared project runner reports a failing test or missing project dependency.
func TestOARTrigger3DeclaredProjectRunnerNoPortablePythonRouting(t *testing.T) {
	bp := stockBlockPlane(t)
	ctx := tools.WithCallReach(t.Context(), toolreach.Offered([]string{"command", "python", "request_tools"}))

	// Pre-invoke of declared project runner is not blocked by PYTHON_TOOL_REQUIRED.
	if err := bp.Evaluate(ctx, oar.AnchorToolPreInvoke, "command", "implement", map[string]any{"command": "external -- python3 -m pytest"}, nil); err != nil {
		t.Fatalf("declared project runner must not be blocked: %v", err)
	}
	// Ordinary non-zero test failure is not routed to portable Python.
	if err := bp.RejectObservation(ctx, "command", "implement", map[string]any{"command": "external -- python3 -m pytest"}, &toolrejection.ToolReject{
		Code: "PROJECT_RUNNER_FAILURE",
		Data: map[string]any{"exit_code": 1, "diagnostic": "ModuleNotFoundError: project_dep"},
	}); err != nil {
		if strings.Contains(err.Error(), "`python(") || strings.Contains(err.Error(), "PYTHON_TOOL_REQUIRED") {
			t.Fatalf("project runner failure must never suggest portable python: %s", err.Error())
		}
	}
}

// Trigger 4: A command pipeline fails in a shell stage (even if a portable Python stage ran earlier).
func TestOARTrigger4CommandPipelineShellStagePreservesCommandInitiator(t *testing.T) {
	bp := stockBlockPlane(t)
	ctx := curationctx.WithSession(t.Context(), curationctx.Session{SessionID: "sess-1", Posture: "ask"})
	ctx = tools.WithCallReach(ctx, toolreach.Offered([]string{"command", "python", "read"}))

	err := bp.RejectObservation(ctx, "command", "implement", map[string]any{"command": "python3 gen.py && rm out.txt"}, &toolrejection.ToolReject{
		Code: "COMMAND_EFFECT_NOT_ALLOWED",
		Data: map[string]any{
			"source_line":             2,
			"source_column":           1,
			"prior_completed_effects": 1,
		},
	})
	refusal, calls := requireRefusal(t, err, "COMMAND_EFFECT_NOT_ALLOWED")
	if !strings.Contains(refusal.Copy["what"], "at 2:1") {
		t.Fatalf("rendered what did not identify stopped shell stage 2:1: %q", refusal.Copy["what"])
	}
	// Pack consumed read_only expected_form from session posture (not profile name!).
	if !strings.Contains(refusal.Copy["fix"], "read_only") {
		t.Fatalf("rendered fix did not reflect read_only posture: %q", refusal.Copy["fix"])
	}
	for _, call := range calls {
		if call.Tool == "python" || call.Tool == "request_tools" {
			t.Fatalf("shell stage failure must not route to python: %+v", calls)
		}
	}
	if strings.Contains(refusal.Copy["fix"], "`python(") {
		t.Fatalf("shell stage failure copy must not suggest python: %q", refusal.Copy["fix"])
	}
}

// Trigger 5: Either tool (python or command) reaches the same denied HTTP request or workspace write.
func TestOARTrigger5DeniedHTTPOrWriteForbidsLanguageSwitchAndSurfacesHeldOp(t *testing.T) {
	bp := stockBlockPlane(t)
	ctx := tools.WithCallReach(t.Context(), toolreach.Offered([]string{"python", "command", "read"}))

	for _, toolName := range []string{"python", "command"} {
		t.Run(toolName+"_http_denied_with_held_op", func(t *testing.T) {
			err := bp.RejectObservation(ctx, toolName, "implement", nil, &toolrejection.ToolReject{
				Code: "HTTP_REQUEST_HOST_DENIED",
				Data: map[string]any{
					"rejection_reason":        "egress destination not approved.",
					"http_requests_started":   1,
					"http_responses_received": 0,
					"held_operation_id":       "hold-http-42",
				},
			})
			refusal, calls := requireRefusal(t, err, "HTTP_REQUEST_HOST_DENIED")
			if len(calls) != 0 {
				t.Fatalf("HTTP denial must not offer tool-switch next_calls: %+v", calls)
			}
			// Pack consumed held_operation_id published into rejection_reason before rendering!
			if !strings.Contains(refusal.Copy["cause"], "hold-http-42") {
				t.Fatalf("rendered cause missing pending held operation hold-http-42: %q", refusal.Copy["cause"])
			}
			if strings.Contains(refusal.Copy["fix"], "`python(") || strings.Contains(refusal.Copy["fix"], "`command(") {
				t.Fatalf("HTTP denial must not suggest switching languages: %q", refusal.Copy["fix"])
			}
		})

		t.Run(toolName+"_workspace_write_denied", func(t *testing.T) {
			err := bp.RejectObservation(ctx, toolName, "implement", nil, &toolrejection.ToolReject{
				Code: "COMMAND_EFFECT_NOT_ALLOWED",
				Data: map[string]any{
					"read_only":     true,
					"source_line":   4,
					"source_column": 2,
				},
			})
			refusal, calls := requireRefusal(t, err, "COMMAND_EFFECT_NOT_ALLOWED")
			if len(calls) != 0 {
				t.Fatalf("workspace write denial must not offer tool-switch next_calls: %+v", calls)
			}
			if !strings.Contains(refusal.Copy["fix"], "read_only") {
				t.Fatalf("rendered fix missing read_only form: %q", refusal.Copy["fix"])
			}
		})
	}

	// Scope negative control: unrelated tools (write, edit, read) are never matched by needsPythonOARRecovery.
	for _, unrelated := range []string{"write", "edit", "read", "fetch_url"} {
		gc := oar.NewGuardContext()
		gc.ObserveToolCall(unrelated, nil)
		if needsPythonOARRecovery(gc, &toolrejection.ToolReject{Code: "COMMAND_EFFECT_NOT_ALLOWED"}) {
			t.Fatalf("needsPythonOARRecovery must not match unrelated tool %q on shared code", unrelated)
		}
	}

	// Posture negative control: profile name containing "readonly" alone does NOT trigger isReadOnlyPosture.
	gcProfileNameOnly := oar.NewGuardContext()
	gcProfileNameOnly.Session.PermissionProfile = "custom_readonly_label"
	if isReadOnlyPosture(gcProfileNameOnly, &toolrejection.ToolReject{Code: "COMMAND_EFFECT_NOT_ALLOWED"}) {
		t.Fatal("isReadOnlyPosture must not infer read-only from profile name substring")
	}
}

// Trigger 6: Tool becomes loadable or absent, or surface changes after a hold.
func TestOARTrigger6SurfaceChangeAfterHoldRendersOnlyCurrentReach(t *testing.T) {
	bp := stockBlockPlane(t)
	reject := &toolrejection.ToolReject{
		Code: "PYTHON_TOOL_REQUIRED",
		Data: map[string]any{
			"command":                 "python3",
			"source_line":             3,
			"source_column":           1,
			"prior_completed_effects": 2,
		},
	}

	// Loadable: request_tools offered + python deferred -> renders loader step + exact need="python".
	ctxLoadable := tools.WithCallReach(t.Context(), toolreach.Freeze(
		[]string{"command", "request_tools"},
		toolsurface.Compile([]string{"command", "request_tools"}, []string{"python"}),
	))
	refLoadable, callsLoadable := requireRefusal(t, bp.RejectObservation(ctxLoadable, "command", "implement", nil, reject), "PYTHON_TOOL_REQUIRED")
	if !strings.Contains(refLoadable.Copy["what"], "3:1") {
		t.Fatalf("original occurrence lost in rendered what: %q", refLoadable.Copy["what"])
	}
	if len(callsLoadable) != 2 || callsLoadable[0].Tool != "request_tools" || callsLoadable[0].Args["need"] != "python" || callsLoadable[1].Tool != "python" {
		t.Fatalf("unexpected loadable next_calls: %+v", callsLoadable)
	}
	if !strings.Contains(refLoadable.Copy["fix"], `request_tools(need="python")`) {
		t.Fatalf("rendered fix missing request_tools(need=\"python\"): %q", refLoadable.Copy["fix"])
	}

	// Loader absent even though python is deferred -> MUST omit loader and unavailable tool!
	ctxNoLoader := tools.WithCallReach(t.Context(), toolreach.Freeze(
		[]string{"command"},
		toolsurface.Compile([]string{"command"}, []string{"python"}),
	))
	refNoLoader, callsNoLoader := requireRefusal(t, bp.RejectObservation(ctxNoLoader, "command", "implement", nil, reject), "PYTHON_TOOL_REQUIRED")
	if !strings.Contains(refNoLoader.Copy["what"], "3:1") {
		t.Fatalf("original occurrence lost when loader absent: %q", refNoLoader.Copy["what"])
	}
	if len(callsNoLoader) != 0 {
		t.Fatalf("expected zero next_calls when loader unavailable, got %+v", callsNoLoader)
	}
	if strings.Contains(refNoLoader.Copy["fix"], "request_tools") || strings.Contains(refNoLoader.Copy["fix"], "`python(") {
		t.Fatalf("rendered fix leaked unavailable tool/loader: %q", refNoLoader.Copy["fix"])
	}

	// Keep unused import check satisfied if pythonevidence constants are referenced directly.
	_ = pythonevidence.StopPythonToolRequired
}
