package oar

import (
	"maps"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oarcore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecoveryRecipesRequireUnrequestedSupportedCapabilities(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalog", err)
	for _, tc := range []struct{ code, field, target string }{
		{"SANDBOX_TRY_READ_PATH", "read_path", "refused_read_grants"},
		{"SANDBOX_TRY_WRITE_ROOT", "write_root", "refused_write_grants"},
		{"SANDBOX_TRY_SOCKET_PATH", "socket_paths", "refused_socket_paths"},
		{"SANDBOX_TRY_CREDENTIAL_STORE", "credential_store", ""},
		{"SANDBOX_TRY_PROCESS_CONTROL", "process_control", "refused_signals"},
		{"SANDBOX_TRY_HOST_EXECUTION", "host_execution", "unsandboxed_refusals"},
		{"SANDBOX_TRY_LOOPBACK_CONNECT", "loopback_connect", "port"},
	} {
		for _, tool := range []string{"command", "read", "unknown_tool"} {
			for _, request := range []string{"unknown", "absent", "declared"} {
				for _, spoof := range []bool{false, true} {
					t.Run(tc.code+"/"+tool+"/"+request, func(t *testing.T) {
						gc := NewGuardContext()
						var args map[string]any
						if request != "unknown" {
							args = map[string]any{}
						}
						if request == "declared" {
							args["capability_request"] = map[string]any{tc.field: true}
						}
						gc.ObserveToolCall(tool, args)
						gc.ObservationData = map[string]any{tc.target: "123", "supports_" + tc.field: spoof, "missing_capability_" + tc.field: spoof, "tool": "command", "tool_args": map[string]any{}}
						gc.Published = map[string]any{"paintedwolf.missing_capability_" + tc.field: spoof}
						facts := activation(gc)
						text := oarcore.RenderCopy(hints.HintCodes[tc.code].Fix, facts)
						want := tool == "command" && request == "absent"
						if strings.Contains(text, "capability_request."+tc.field) != want {
							t.Fatalf("recipe=%q, want available=%t", text, want)
						}
						if want && tc.target != "" {
							delete(gc.ObservationData, tc.target)
							if text := strings.TrimSpace(oarcore.RenderCopy(hints.HintCodes[tc.code].Fix, activation(gc))); text != "" {
								t.Fatalf("missing target acquired a recipe: %s", text)
							}
						}
					})
				}
			}
		}
	}
}

func TestUnknownRecoveryHasNoInventedRemedy(t *testing.T) {
	setupAuthorCheckRenderer(t)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalog", err)
	for _, code := range []string{"COMMAND_EXEC_DENIED", "COMMAND_NOT_FOUND", "COMMAND_NATIVE_FORM_UNSUPPORTED", "COMMAND_PLATFORM_UNAVAILABLE", "COMMAND_LIMIT_EXCEEDED", "COMMAND_OPERAND_INVALID", "TOOL_OWNER_FAILED", "TOOL_OWNER_INTERRUPTED", "TOOL_ARGS_INVALID", "SANDBOX_REFUSAL_REPORT_SILENT", "SANDBOX_BOUNDARY_REFUSED", "COMMAND_REVISION_CONFLICT", "VERIFY_UNVERIFIABLE", "HOST_RESOURCE_POLICY_DENIED", "REMOTE_PACKAGE_EXECUTION_DESTINATION_DENIED", "DESKTOP_CAPTURE_DISALLOWED"} {
		t.Run(code, func(t *testing.T) {
			gc := NewGuardContext()
			gc.ObserveToolCall("command", map[string]any{})
			gc.ObservationData = map[string]any{"reason": "unknown cause", "path": "tool", "limit_id": "child_executions", "maximum": 4, "observed": 5}
			facts := activation(gc)
			entry := hints.HintCodes[code]
			copy := map[string]string{"what": oarcore.RenderCopy(entry.What, facts), "cause": oarcore.RenderCopy(entry.Cause, facts), "why": oarcore.RenderCopy(entry.Why, facts), "fix": oarcore.RenderCopy(entry.Fix, facts), "instead": oarcore.RenderCopy(entry.Instead, facts)}
			if strings.TrimSpace(copy["fix"]) != "" || strings.TrimSpace(copy["instead"]) != "" {
				t.Fatalf("unknown cause acquired a remedy: %#v", copy)
			}
			text, err := guidance.RenderPolicyCopy(t.Context(), code, "block", maps.Clone(copy), gc.ObservationData)
			testutil.FailErr(t, "render observation", err)
			if strings.Contains(text, "Fix:") || strings.Contains(text, "Next:") || !strings.Contains(text, "Code: "+code) {
				t.Fatalf("abstention was lost in delivery: %s", text)
			}
		})
	}
}

func TestArgumentRepairCannotCrossReceivingToolIdentity(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalog", err)
	for _, schemaTool := range []string{"", "read", "command"} {
		gc := NewGuardContext()
		gc.ObserveToolCall("command", map[string]any{"command": "earlier; read"})
		gc.ObservationData = map[string]any{
			"tool": "read", "argument_schema_tool": schemaTool,
			"replacement_args_json":        `{"path":"input"}`,
			"argument_schema_matches_tool": true,
		}
		text := oarcore.RenderCopy(hints.HintCodes["TOOL_ARGS_INVALID"].Fix, activation(gc))
		if strings.Contains(text, `"command"`) != (schemaTool == "command") {
			t.Fatalf("schema %q crossed command identity: %s", schemaTool, text)
		}
	}
}

func TestRevisionRecoveryRequiresAnObservedIdentityChange(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalog", err)
	for _, revisions := range [][2]string{{"", ""}, {"before", ""}, {"", "after"}, {"same", "same"}, {"before", "after"}} {
		gc := NewGuardContext()
		gc.ObserveToolCall("command", map[string]any{})
		gc.ObservationData = map[string]any{"path": "input", "expected_revision": revisions[0], "observed_revision": revisions[1], "revision_change_observed": true}
		fix := strings.TrimSpace(oarcore.RenderCopy(hints.HintCodes["COMMAND_REVISION_CONFLICT"].Fix, activation(gc)))
		known := revisions[0] != "" && revisions[1] != "" && revisions[0] != revisions[1]
		if (fix != "") != known {
			t.Fatalf("revisions %v acquired the wrong recovery: %s", revisions, fix)
		}
	}
}

func TestResourceFeedbackDistinguishesUnknownFromMeasuredZero(t *testing.T) {
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load recovery catalog", err)
	for _, values := range []map[string]any{nil, {"maximum": 0}, {"maximum": 0, "observed": 0}, {"maximum": 8, "observed": 9}, {"maximum": "8", "observed": 9}} {
		gc := NewGuardContext()
		gc.ObserveToolCall("command", map[string]any{})
		gc.ObservationData = maps.Clone(values)
		if gc.ObservationData == nil {
			gc.ObservationData = map[string]any{}
		}
		gc.ObservationData["limit_observation_known"] = true
		text := oarcore.RenderCopy(hints.HintCodes["COMMAND_LIMIT_EXCEEDED"].What, activation(gc))
		_, maxKnown := values["maximum"].(int)
		_, observedKnown := values["observed"].(int)
		if strings.Contains(text, "Observed") != (maxKnown && observedKnown) {
			t.Fatalf("values %v became fabricated observations: %s", values, text)
		}
		if maxKnown && observedKnown && values["maximum"] == 0 && !strings.Contains(text, "Observed 0; maximum 0") {
			t.Fatalf("measured zero was lost: %s", text)
		}
	}
}
