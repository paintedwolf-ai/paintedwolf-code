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

	// Each branch must carry the identifiers the caller needs to repair the
	// call: the field, where it was written, and where it belongs.
	cases := []struct {
		name      string
		data      map[string]any
		wantWhat  string
		wantCause string
		wantFix   string
	}{
		{
			name: "misplaced at the top level",
			data: map[string]any{
				"field":            "host_resources",
				"misplaced_fields": []string{"host_resources"},
				"found_under":      "",
				"belongs_under":    "capability_request",
			},
			wantWhat:  "`host_resources`",
			wantCause: "`capability_request`",
			wantFix:   "`capability_request`",
		},
		{
			name: "did you mean typo",
			data: map[string]any{
				"field":        "timeout",
				"did_you_mean": "timeout_ms",
			},
			wantWhat:  "`timeout_ms`",
			wantCause: "`timeout`",
			wantFix:   "`timeout_ms`",
		},
		{
			name: "encoded JSON string",
			data: map[string]any{
				"field":         "body_json",
				"json_encoded":  true,
				"expected_type": "object",
			},
			wantWhat:  "`body_json`",
			wantCause: "object",
			wantFix:   "`body_json`",
		},
		{
			name: "malformed JSON text",
			data: map[string]any{
				"field":            "verdict",
				"json_malformed":   true,
				"json_open_paths":  []string{"verdict"},
				"expected_type":    "object",
				"misplaced_fields": []string{"set_asides", "threat_model", "verdict"},
				"found_under":      "verdict.coverage",
				"belongs_under":    "verdict",
				"close_before":     "set_asides",
			},
			wantWhat:  "`verdict`",
			wantCause: "`verdict.coverage`",
			wantFix:   "`set_asides`",
		},
		{
			name: "malformed JSON text at an offset",
			data: map[string]any{
				"field":                    "body_json",
				"json_malformed":           true,
				"json_unexpected_token_at": 42,
				"expected_type":            "object",
			},
			wantWhat:  "`body_json`",
			wantCause: "42",
			wantFix:   "`body_json`",
		},
		{
			name: "conflict keys",
			data: map[string]any{
				"conflict_keys": []string{"operations", "start_line"},
			},
			wantWhat:  "operations, start_line",
			wantCause: "mutual exclusivity",
			wantFix:   "Correct the field type, required value, or structure",
		},
		{
			name: "replacement args json",
			data: map[string]any{
				"field":                 "host_resources",
				"misplaced_fields":      []string{"host_resources"},
				"belongs_under":         "capability_request",
				"replacement_args_json": "{\n  \"command\": \"colima start\"\n}",
			},
			wantWhat:  "`host_resources`",
			wantCause: "`capability_request`",
			wantFix:   "```json\n{\n  \"command\": \"colima start\"\n}\n```",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gc := NewGuardContext()
			gc.Invocation.Tool = "command"
			gc.ObservedRejectCode = "TOOL_ARGS_INVALID"
			gc.Invocation.ArgValidationErrors = []string{"TOOL_ARGS_INVALID"}
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
