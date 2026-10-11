package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolfeedback"

	"github.com/lycaon/lycaon/internal/capabilityrequest"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// [OAR-EVAL-8] Shared validation categories never select another failure's
// recovery or side effects. Exercise the stock decision before renderer fallback.
func TestCatalogIntrinsicRejectionsKeepTheirIdentity(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, tc := range []struct{ tool, code string }{
		{"read", "READ_FILE_TOO_LARGE"},
		{"summarize", "READ_FILE_TOO_LARGE"},
		{"diff", "READ_FILE_TOO_LARGE"},
		{"jq_edit", "READ_FILE_TOO_LARGE"},
		{"edit", "EDIT_FILE_TOO_LARGE"},
		{"replace_lines", "EDIT_FILE_TOO_LARGE"},
		{"code_rewrite", "EDIT_FILE_TOO_LARGE"},
		{"jq_edit", "EDIT_FILE_TOO_LARGE"},
		{"write", "EDIT_FILE_TOO_LARGE"},
		{"restore_version", "EDIT_FILE_TOO_LARGE"},
		{"jq", "READ_BINARY_DENIED"},
		{"edit", "READ_BINARY_DENIED"},
		{"replace_lines", "READ_BINARY_DENIED"},
		{"code_rewrite", "READ_BINARY_DENIED"},
		{"read", "SANDBOX_APPROVAL_UNAVAILABLE"},
		{"secret_generate", "SANDBOX_APPROVAL_UNAVAILABLE"},
		{"measure_page", "PAGE_NOT_FOUND"},
		{"measure_page", "PAGE_NOT_RUNNING"},
		{"view_image", "IMAGE_FORMAT_UNSUPPORTED"},
		{"view_image", "IMAGE_CORRUPTED"},
		{"view_image", "IMAGE_DIMENSIONS_EXCEEDED"},
		{"command", "HOST_APPROVAL_PLAN_INVALID"},
		{"verify", "HOST_APPROVAL_PLAN_INVALID"},
		{"page_open", "HOST_APPROVAL_PLAN_INVALID"},
		{"http_request", "HOST_APPROVAL_PLAN_INVALID"},
		{"read", "HOST_APPROVAL_PLAN_INVALID"},
		{"secret_generate", "HOST_APPROVAL_PLAN_INVALID"},

		{"git_commit", "GIT_COMMIT_PATH_DENIED"},
		{"git_checkout", "GIT_COMMIT_PATH_DENIED"},
		{"git_merge", "GIT_COMMIT_PATH_DENIED"},
		{"git_stash", "GIT_COMMIT_PATH_DENIED"},

		{"delegate_dispatch", "COORDINATOR_DELEGATE_DISPATCH_USE_TASK"},
		{"delegate_dispatch", "DELEGATE_DISPATCH_LEG_REQUIRED"},
		{"answer_decision", "DECISION_CHANGED"},
		{"answer_decision", "DECISION_JOB_ID_MISMATCH"},
		{"worker_cancel", "WORKER_CANCEL_TERMINAL"},
		{"task", "TASK_DECISION_PENDING"},
		{"task", "WORKER_TYPE_UNAVAILABLE"},
		{"promote_overlay", "OVERLAY_PROMOTE_NOT_FOUND"},
		{"read", "PROJECT_HAS_NO_ROOTS"},
		{"workflow_transition", "WORKFLOW_TRANSITION_INACTIVE"},
		{"read", "READ_ARGS_CONFLICT"},
		{"capture_page", "CAPTURE_ACTIONS_BOUNDS"},
		{"page_act", "CAPTURE_ACTIONS_BOUNDS"},
		{"page_act", "CAPTURE_TARGET_INVALID"},
		{"page_open", "CAPTURE_NAVIGATION_DENIED"},
		{"page_open", "CAPTURE_PROJECT_DIR_INVALID"},
		{"page_open", "CAPTURE_PROJECT_DIR_MISSING"},
		{"page_open", "CAPTURE_PROCESS_NOT_RUNNING"},
		{"page_open", "CAPTURE_SERVE_FAILED"},
		{"page_open", "CAPTURE_URL_NOT_LOOPBACK"},
		{"page_snapshot", "CAPTURE_OUTPUT_OVERSIZED"},
		{"capture_page", "CAPTURE_OUTPUT_OVERSIZED"},
		{"page_open", "CAPTURE_URL_UNREACHABLE"},
		{"page_open", "BROWSER_ENGINE_UNAVAILABLE"},
		{"render_view", "RENDER_ASSET_DENIED"},
		{"render_view", "RENDER_MARKUP_FORBIDDEN"},
		{"grep", "GREP_DEADLINE_EXCEEDED"},
		{"find", "SURVEY_INVENTORY_WARMING"},
		{"http_request", "HTTP_REQUEST_COOKIE_NOT_HELD"},
		{"http_request", "HTTP_REQUEST_COOKIE_JAR_FAILED"},
		{"wait", "WAIT_PROBE_SECRET_UNSUPPORTED"},
		{"extend_worker_budget", "WORKER_BUDGET_EXTEND_NOT_RUNNING"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			gc := oar.NewGuardContext()
			gc.ObserveToolCall(tc.tool, nil)
			toolfeedback.ApplyToolRejectObservations(gc, &toolrejection.ToolReject{Code: tc.code})
			result, err := bp.Pipeline.EvaluateBlock(t.Context(), oar.AnchorToolRejected, gc)
			testutil.FailErr(t, "evaluate catalog rejection", err)
			if result == nil || result.Decision == nil || result.Decision.Code != tc.code {
				t.Fatalf("decision for %s = %+v", tc.code, result)
			}
		})
	}
}

func TestCatalogCapabilityRecoveryUsesInvocationContract(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, capabilities := range []toolcontract.Capability{0, toolcontract.CapabilityLoopbackConnect} {
		contract := toolcontract.Contract{Capabilities: capabilities}
		reject := capabilityrequest.ValidateCapabilityContract(contract, map[string]any{
			"capability_request": map[string]any{"direct_ip": map[string]any{}},
		})
		if reject == nil {
			t.Fatal("unsupported direct IP accepted")
		}
		// A dynamically narrowed definition overrides the compiled command contract.
		err := bp.RejectFromObservation(t.Context(), oar.AnchorToolRejected, "command", "implement", nil, reject)
		if err == nil {
			t.Fatal("missing capability refusal")
		}
		text := err.Error()
		if strings.Contains(text, "local_listen") || strings.Contains(text, "socket_paths") {
			t.Fatalf("recovery widened invocation capabilities: %s", text)
		}
		if strings.Contains(text, "loopback_connect") != contract.Supports(toolcontract.CapabilityLoopbackConnect) {
			t.Fatalf("wrong client capability recovery: %s", text)
		}
	}
}

func TestCatalogMCPHostRefusalsAreNotServerFailures(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, code := range []string{
		"MCP_CONSENT_STATE_UNAVAILABLE", "MCP_TRANSPORT_UNAVAILABLE", "MCP_TOOL_ERROR",
		"OUTBOUND_SECRET_DENIED", "OUTBOUND_SECRET_SCREEN_FAILED",
	} {
		t.Run(code, func(t *testing.T) {
			gc := oar.NewGuardContext()
			gc.ObserveToolCall("mcp_fixture_action", nil)
			toolfeedback.ApplyToolRejectObservations(gc, &toolrejection.ToolReject{Code: code})
			if gc.ObservedRejectCode != code {
				t.Fatalf("host refusal changed to %s", gc.ObservedRejectCode)
			}
			result, err := bp.Pipeline.EvaluateBlock(t.Context(), oar.AnchorToolRejected, gc)
			testutil.FailErr(t, "evaluate MCP host refusal", err)
			if result == nil || result.Decision == nil || result.Decision.Code != code {
				t.Fatalf("decision for %s = %+v", code, result)
			}
		})
	}
}

// Every intrinsic catalog rejection resolves through the production OAR
// evaluator. A renderer fallback cannot certify a rule whose selector is stale.
func TestCatalogEveryIntrinsicRejectionResolvesBeforeFallback(t *testing.T) {
	testutil.SkipIfShort(t, "walks every intrinsic catalog rejection through the OAR evaluator; test:oar and test:full run it")
	bp := stockBlockPlane(t)
	checked := 0
	for _, rule := range bp.Pipeline.Rules().All() {
		if rule.Anchor != oar.AnchorToolRejected || rule.When != `paintedwolf.rejection_code == "`+rule.ID+`"` {
			continue
		}
		checked++
		t.Run(rule.ID, func(t *testing.T) {
			calls := append([]string(nil), rule.Selector["tool"]...)
			if len(calls) == 0 {
				calls = []string{"read"}
			}
			for _, tool := range calls {
				cases := rule.Scenarios
				if len(cases) == 0 {
					t.Fatal("intrinsic rejection has no authored scenarios")
				}
				for _, scenario := range cases {
					t.Run(tool+"/"+scenario.ID, func(t *testing.T) {
						gc := oar.NewGuardContext()
						gc.Session.SessionID = t.Name()
						gc.ObserveToolCall(tool, nil)
						toolfeedback.ApplyToolRejectObservations(gc, &toolrejection.ToolReject{Code: rule.ID, Data: scenario.Vars})
						result, err := bp.Pipeline.EvaluateBlock(t.Context(), oar.AnchorToolRejected, gc)
						testutil.FailErr(t, "evaluate intrinsic rejection", err)
						if result.Decision == nil || result.Decision.Code != rule.ID || result.Decision.Effect != rule.Effect {
							t.Fatalf("observed %s on %s resolved as %+v", rule.ID, tool, result.Decision)
						}
						if len(result.Decision.Copy) == 0 {
							t.Fatal("intrinsic rejection lost its evaluated recovery copy")
						}
					})
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("no intrinsic catalog rejections exercised")
	}
}

func TestNavigationRefusalRetainsMeasuredTargetAcrossCallers(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, tool := range []string{"capture_page", "measure_page", "page_open"} {
		target := "http://127.0.0.1:5678/literal-{{ value }}"
		cause := "net::ERR_CONNECTION_REFUSED {{ untouched }}"
		err := bp.RejectFromObservation(t.Context(), oar.AnchorToolRejected, tool, "implement", nil, &toolrejection.ToolReject{Code: "CAPTURE_URL_UNREACHABLE", Data: map[string]any{"navigation_url": target, "navigation_error": cause}})
		if err == nil || !strings.Contains(err.Error(), target) || !strings.Contains(err.Error(), cause) {
			t.Fatalf("%s lost original navigation facts: %v", tool, err)
		}
	}
}
