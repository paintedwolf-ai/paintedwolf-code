package oar

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestEvaluateBlockToolArgsInvalidCopyBranches(t *testing.T) {
	ensureCatalog(t)
	l, err := NewLoader(schemaDir(t))
	testutil.FailErr(t, "loader", err)
	rs, err := l.LoadDir(hintsDir(t))
	testutil.FailErr(t, "load", err)
	p := NewGuardPipeline(rs, l, NewCounterStore())
	p.EnableAnchor(AnchorToolRejected)

	cases := []struct {
		name         string
		data         map[string]any
		wantWhat     string
		wantCause    string
		wantFix      string
	}{
		{
			name: "relocated field",
			data: map[string]any{
				"relocated_field": "host_resources",
				"nested_under":    "capability_request",
				"expected_path":   "capability_request.host_resources",
			},
			wantWhat:  "Argument `host_resources` was placed at root; it belongs under `capability_request`",
			wantCause: "The tool schema does not accept `host_resources` at root; it must be nested under `capability_request`.",
			wantFix:   "nested inside `capability_request`",
		},
		{
			name: "did you mean typo",
			data: map[string]any{
				"field":        "timeout",
				"did_you_mean": "timeout_ms",
			},
			wantWhat:  "Unknown argument `timeout`: did you mean `timeout_ms`?",
			wantCause: "The property `timeout` is not defined on this tool; `timeout_ms` is declared.",
			wantFix:   "Use `timeout_ms` instead of `timeout`.",
		},
		{
			name: "unparsed JSON string",
			data: map[string]any{
				"field":         "body_json",
				"unparsed_json": true,
				"expected_type": "object",
			},
			wantWhat:  "Argument `body_json` was passed as a JSON string instead of a native object",
			wantCause: "The tool schema expects a structured JSON object, but received a string literal containing escaped JSON.",
			wantFix:   "Correct the field type, required value, or structure",
		},
		{
			name: "conflict keys",
			data: map[string]any{
				"conflict_keys": []string{"operations", "start_line"},
			},
			wantWhat:  "Conflicting arguments provided: pass only one of operations, start_line",
			wantCause: "The supplied arguments violate mutual exclusivity.",
			wantFix:   "Correct the field type, required value, or structure",
		},
		{
			name: "replacement args json",
			data: map[string]any{
				"relocated_field":       "host_resources",
				"nested_under":          "capability_request",
				"replacement_args_json": "{\n  \"command\": \"colima start\"\n}",
			},
			wantWhat:  "Argument `host_resources` was placed at root; it belongs under `capability_request`",
			wantCause: "The tool schema does not accept `host_resources` at root; it must be nested under `capability_request`.",
			wantFix:   "Reissue the call with repaired arguments:\n```json\n{\n  \"command\": \"colima start\"\n}\n```",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gc := NewGuardContext()
			gc.Tool = "command"
			gc.ObservedRejectCode = "TOOL_ARGS_INVALID"
			gc.ArgValidationErrors = []string{"TOOL_ARGS_INVALID"}
			gc.PutRejectData("TOOL_ARGS_INVALID", tc.data)

			res, err := p.EvaluateBlock(context.Background(), AnchorToolRejected, gc)
			testutil.FailErr(t, "EvaluateBlock", err)
			if !res.Enforced || res.Decision == nil {
				t.Fatalf("want 1 enforced decision, got %#v", res)
			}
			if res.Decision.Code != "TOOL_ARGS_INVALID" {
				t.Fatalf("code = %q, want TOOL_ARGS_INVALID", res.Decision.Code)
			}
			copyObj := res.Decision.Copy
			if copyObj == nil {
				t.Fatalf("missing copy in decision: %#v", res.Decision)
			}
			what := copyObj["what"]
			if !strings.Contains(what, tc.wantWhat) {
				t.Errorf("copy.what = %q, want to contain %q", what, tc.wantWhat)
			}
			cause := copyObj["cause"]
			if !strings.Contains(cause, tc.wantCause) {
				t.Errorf("copy.cause = %q, want to contain %q", cause, tc.wantCause)
			}
			fix := copyObj["fix"]
			if !strings.Contains(fix, tc.wantFix) {
				t.Errorf("copy.fix = %q, want to contain %q", fix, tc.wantFix)
			}
		})
	}
}
