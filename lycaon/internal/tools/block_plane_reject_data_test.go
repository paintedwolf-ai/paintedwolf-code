package tools

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// stockBlockPlane mirrors production policy rendering.
func stockBlockPlane(t *testing.T) *BlockPlane {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Join(filepath.Dir(file), "..", "..")
	repoRoot := filepath.Join(moduleRoot, "..")
	testutil.FailErr(t, "install catalog", anchorcatalog.InstallFile(
		filepath.Join(moduleRoot, "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")))

	l, err := oar.NewLoader(filepath.Join(repoRoot, "schemas"))
	testutil.FailErr(t, "loader", err)
	rs, err := l.LoadEffectivePolicy()
	testutil.FailErr(t, "load stock", err)
	pipeline := oar.NewGuardPipeline(rs, l, oar.NewCounterStore())
	// Enable both anchors used by tool rejections.
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)

	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hints", err)
	guidance.SetGuidanceRenderer(prompts.NewGuidanceRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})))
	return &BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(guidance.NewStaticRejectFormatter(cfg), nil)}
}

func TestRejectFromObservationInterpolatesPathAndID(t *testing.T) {
	bp := stockBlockPlane(t)

	pathErr := bp.RejectFromObservation(context.Background(), oar.AnchorToolRejected, "read", "implement", nil, &ToolReject{
		Code: "READ_PATH_NOT_FOUND",
		Data: map[string]any{"path": "weather_cli/cli.py"},
	})
	if pathErr == nil {
		t.Fatal("expected READ_PATH_NOT_FOUND reject")
	}
	pathMsg := pathErr.Error()
	if strings.Contains(pathMsg, "{{ path }}") || strings.Contains(pathMsg, "{{path}}") {
		t.Fatalf("path must interpolate; got:\n%s", pathMsg)
	}
	if !strings.Contains(pathMsg, "weather_cli/cli.py") || !strings.Contains(pathMsg, "READ_PATH_NOT_FOUND") {
		t.Fatalf("reject missing path/code:\n%s", pathMsg)
	}

	idErr := bp.RejectFromObservation(context.Background(), oar.AnchorToolRejected, "terminal_send", "implement", nil, &ToolReject{
		Code: "TERMINAL_NOT_RUNNING",
		Data: map[string]any{"id": "65b7e1ff-ae67-46e9-b5b8-db7e5b42ef37", "reason": "not_running"},
	})
	if idErr == nil {
		t.Fatal("expected TERMINAL_NOT_RUNNING reject")
	}
	idMsg := idErr.Error()
	if strings.Contains(idMsg, "{{ id }}") || strings.Contains(idMsg, "{{id}}") {
		t.Fatalf("id must interpolate; got:\n%s", idMsg)
	}
	if !strings.Contains(idMsg, "65b7e1ff-ae67-46e9-b5b8-db7e5b42ef37") || !strings.Contains(idMsg, "TERMINAL_NOT_RUNNING") {
		t.Fatalf("reject missing id/code:\n%s", idMsg)
	}
}

func TestRejectFromObservationMCPDeclaredCodeUsesMCPCallFailed(t *testing.T) {
	bp := stockBlockPlane(t)

	err := bp.RejectFromObservation(context.Background(), oar.AnchorToolRejected, "mcp_fixture_fail_coded", "coordinator", nil, &ToolReject{
		Code: "MCP_SERVER_FIXTURE_MCP_DENIED",
		Data: map[string]any{"detail": "fixture denied", "provider": "fixture", "tool": "fail_coded"},
	})
	if err == nil {
		t.Fatal("expected MCP_CALL_FAILED reject")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Code: MCP_CALL_FAILED") || !strings.Contains(msg, "FIXTURE_MCP_DENIED") {
		t.Fatalf("want MCP_CALL_FAILED citing machine code:\n%s", msg)
	}

	err = bp.RejectFromObservation(context.Background(), oar.AnchorToolRejected, "mcp_fixture_fail_plain", "coordinator", nil, &ToolReject{
		Code: "MCP_TOOL_ERROR",
		Data: map[string]any{"detail": "plain failure"},
	})
	if err == nil {
		t.Fatal("expected MCP_TOOL_ERROR reject")
	}
	if !strings.Contains(err.Error(), "Code: MCP_TOOL_ERROR") {
		t.Fatalf("want MCP_TOOL_ERROR:\n%s", err.Error())
	}
}

func TestRejectFromObservationFallbackPreservesTypedCause(t *testing.T) {
	bp := stockBlockPlane(t)
	bp.Renderer = nil
	reject := &ToolReject{
		Code: "HTTP_REQUEST_FAILED", FailureClass: api.FailureClassOwnerError, Retryable: true,
		Data: map[string]any{"reason": "remote ended the response"},
	}
	err := bp.RejectFromObservation(context.Background(), oar.AnchorToolRejected, "http_request", "implement", nil, reject)
	if err == nil {
		t.Fatal("expected HTTP_REQUEST_FAILED reject")
	}
	if got := AsToolReject(err); got != reject {
		t.Fatalf("typed cause = %#v, want original reject %#v", got, reject)
	}
}

func TestHTTPRejectCopyPreservesReasonAndRetryDecision(t *testing.T) {
	bp := stockBlockPlane(t)
	recoveries := map[bool]string{}
	for _, retryable := range []bool{false, true} {
		for _, reason := range []string{"remote ended the response", "fixture permanently unavailable"} {
			reject := &ToolReject{Code: "HTTP_REQUEST_FAILED", Retryable: retryable, Data: map[string]any{"reason": reason, "retryable": retryable}}
			err := bp.RejectFromObservation(t.Context(), oar.AnchorToolRejected, "http_request", "implement", nil, reject)
			if err == nil || !strings.Contains(err.Error(), reason) || AsToolReject(err) != reject {
				t.Fatalf("lost failure facts: %v", err)
			}
			refusal, ok := guidance.RefusalFromError(err)
			if !ok || refusal.Copy == nil || AsToolReject(err).Retryable != retryable {
				t.Fatalf("lost retry metadata or frozen recovery: %v", err)
			}
			recoveries[retryable] = refusal.Copy["fix"]
		}
	}
	if recoveries[false] == "" || recoveries[true] == "" || recoveries[false] == recoveries[true] {
		t.Fatalf("typed retryability did not select distinct recovery: %v", recoveries)
	}
	reject := &ToolReject{Code: "HTTP_REQUEST_HOST_DENIED", Data: map[string]any{"reason": "loopback address was not authorized"}}
	err := bp.RejectFromObservation(t.Context(), oar.AnchorToolRejected, "http_request", "implement", nil, reject)
	if err == nil || !strings.Contains(err.Error(), "loopback address was not authorized") || AsToolReject(err) != reject {
		t.Fatalf("lost destination denial reason: %v", err)
	}
}

func TestFileRejectsPreserveObservedFailure(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, tool := range []string{"read", "edit", "write", "replace_lines", "code_rewrite", "diff"} {
		t.Run(tool+"/missing path", func(t *testing.T) {
			reject := &ToolReject{Code: "READ_PATH_NOT_FOUND", Data: map[string]any{"path": "src/missing.py"}}
			err := bp.RejectFromObservation(context.Background(), oar.AnchorToolRejected, tool, "implement", nil, reject)
			if err == nil {
				t.Fatal("missing path must produce actionable feedback")
			}
			if !strings.Contains(err.Error(), "Code: READ_PATH_NOT_FOUND") || !strings.Contains(err.Error(), "src/missing.py") {
				t.Fatalf("missing path misreported: %v", err)
			}
			if AsToolReject(err) != reject {
				t.Fatalf("typed cause changed: %v", err)
			}
		})
	}
	t.Run("edit/missing substring", func(t *testing.T) {
		reject := &ToolReject{Code: "EDIT_OLD_STRING_NOT_FOUND", Data: map[string]any{
			"path": "src/existing.py", "old_string_chars": 42, "total_lines": 120,
		}}
		err := bp.RejectFromObservation(context.Background(), oar.AnchorToolRejected, "edit", "implement", nil, reject)
		if err == nil {
			t.Fatal("missing substring must produce actionable feedback")
		}
		for _, want := range []string{"Code: EDIT_OLD_STRING_NOT_FOUND", "src/existing.py", "42 chars", "120 lines"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("missing substring feedback lacks %q: %v", want, err)
			}
		}
	})
}

func TestArgumentCheckFactsExcludeOperationalFailures(t *testing.T) {
	bp := stockBlockPlane(t)
	invalid := ValidateCallArguments("read", map[string]any{"count": "many"}, map[string]any{
		"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer"}},
	}, ToolContext{})
	if invalid == nil || !invalid.ArgumentValidation {
		t.Fatal("expected a real schema validation failure")
	}
	gc := oar.NewGuardContext()
	applyToolRejectObservations(gc, invalid)
	if len(gc.Invocation.ArgValidationErrors) != 1 || gc.Invocation.ArgValidationErrors[0] != invalid.Code || gc.Invocation.ArgValidationReason == "" {
		t.Fatalf("[OAR-PROF-3] lost observed argument check: %#v", gc)
	}
	operational := &ToolReject{Code: "HTTP_REQUEST_FAILED", Data: map[string]any{
		"reason": "response transfer interrupted", "field": "response",
		"arg_validation_reason": "untrusted metadata", "arg_validation_errors": []string{"invented"},
	}}
	gc = oar.NewGuardContext()
	applyToolRejectObservations(gc, operational)
	if len(gc.Invocation.ArgValidationErrors) != 0 || gc.Invocation.ArgValidationReason != "" || gc.Invocation.ArgValidationField != "" {
		t.Fatal("[OAR-PROF-3] operational failure was reported as an argument check")
	}
	err := bp.RejectFromObservation(t.Context(), oar.AnchorToolRejected, "http_request", "implement", nil, operational)
	if err == nil || AsToolReject(err) != operational || !strings.Contains(err.Error(), "response transfer interrupted") {
		t.Fatalf("lost operational diagnostics or typed cause: %v", err)
	}
}
