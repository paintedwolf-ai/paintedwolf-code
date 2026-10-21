package transcript

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOversizedHostFeedbackRetainsDiagnosticFacts(t *testing.T) {
	for _, tc := range []struct {
		tool, code string
		details    map[string]any
	}{
		{"write", "TOOL_ARGS_INVALID", map[string]any{"field": "payload", "expected_type": "object", "json_malformed": true, "json_unexpected_token_at": 17}},
		{"edit", "TEXT_WRITE_CONFLICT", map[string]any{"path": "src/app.go", "text_base_changed": true}},
		{"browser", "CAPTURE_ACTION_FAILED", map[string]any{"index": 2, "completed_actions": []any{map[string]any{"index": 0, "type": "click"}}, "interactive_truncated": true}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			msg := feedbackMessage()
			msg.ID = "durable-result"
			msg.ToolResult.Tool = tc.tool
			tc.details["large"] = strings.Repeat("x", maxToolFeedbackBytes)
			msg.ToolResult.Feedback = []api.ToolFeedback{{Code: tc.code, Details: tc.details}}
			before, err := json.Marshal(msg)
			testutil.FailErr(t, "snapshot canonical result", err)
			parts := toolFeedbackParts(msg)
			var payload struct {
				Feedback []struct {
					Details   map[string]any `json:"details"`
					Omitted   bool           `json:"details_omitted"`
					Reference string         `json:"result_message_id"`
				} `json:"tool_feedback"`
			}
			testutil.FailErr(t, "decode feedback", json.Unmarshal([]byte(parts[0].Content), &payload))
			got := payload.Feedback[0]
			for key, want := range tc.details {
				if key == "large" {
					continue
				}
				a, _ := json.Marshal(got.Details[key])
				b, _ := json.Marshal(want)
				if string(a) != string(b) {
					t.Errorf("%s = %s, want %s", key, a, b)
				}
			}
			if !got.Omitted || got.Reference != msg.ID || got.Details["large"] != nil {
				t.Fatalf("omission accounting lost: %+v", got)
			}
			encoded, _ := json.Marshal(got.Details)
			if len(encoded) > 2048 {
				t.Fatalf("diagnostics exceed 2 KiB: %d", len(encoded))
			}
			after, _ := json.Marshal(msg)
			if string(before) != string(after) || parts[0].Authority != api.ContentAuthorityNone {
				t.Fatal("projection changed canonical data or its authority")
			}
		})
	}
}

func TestHostDiagnosticsBoundEncodedValuesAndPreserveNumbers(t *testing.T) {
	rows := make([]any, 30)
	for i := range rows {
		rows[i] = map[string]any{"field": strings.Repeat("界", 200), "kind": "missing"}
	}
	details := map[string]any{
		"reason": strings.Repeat("\x00", 300),
		"field":  "payload", "revision": json.Number("9007199254740993"), "issues": rows,
		"replacement_calls": []any{map[string]any{"tool": "write", "args": map[string]any{"content": "must stay whole"}}},
	}
	got := boundedHostDiagnostics(details, maxHostDiagnosticBytes)
	raw, err := json.Marshal(got)
	testutil.FailErr(t, "encode diagnostics", err)
	if len(raw) > maxHostDiagnosticBytes || got["reason"] != nil || got["replacement_calls"] != nil || got["field"] != "payload" {
		t.Fatalf("bounds or identity lost: %s", raw)
	}
	if got["revision"] != json.Number("9007199254740993") {
		t.Fatalf("integer precision lost: %#v", got["revision"])
	}
	kept := got["issues"].([]any)
	if len(kept) == 0 || len(kept) > 8 || got["issues_omitted"] != len(rows)-len(kept) {
		t.Fatalf("list accounting lost: %#v", got)
	}
}

func TestHostDiagnosticsBoundNestedRepairs(t *testing.T) {
	details := map[string]any{"field": "deep"}
	for i := 0; i < 25; i++ {
		details = map[string]any{"field": "outer", "repairs": []any{map[string]any{"code": "TOOL_ARGS_INVALID", "details": details}}}
	}
	got := boundedHostDiagnostics(details, maxVerdictDiagnosticBytes)
	for depth := 0; depth <= maxDiagnosticRepairDepth; depth++ {
		rows := got["repairs"].([]any)
		if depth == maxDiagnosticRepairDepth {
			if len(rows) != 0 || got["repairs_omitted"] != 1 {
				t.Fatalf("depth bound lost: %#v", got)
			}
			break
		}
		if len(rows) != 1 {
			t.Fatalf("lost outer repair at depth %d: %#v", depth, got)
		}
		row := rows[0].(map[string]any)
		if row["code"] != "TOOL_ARGS_INVALID" || row["details_omitted"] != true {
			t.Fatalf("repair identity lost: %#v", row)
		}
		got = row["details"].(map[string]any)
	}
}

func TestHostDiagnosticsOmitDeepValuesAndInvalidSources(t *testing.T) {
	var value any = "leaf"
	for i := 0; i < 20; i++ {
		value = map[string]any{"nested": value}
	}
	got := boundedHostDiagnostics(map[string]any{"timeout": value, "field": "keep"}, maxHostDiagnosticBytes)
	if got["timeout"] != nil || got["field"] != "keep" {
		t.Fatalf("nested value leaked: %#v", got)
	}
	if got := boundedHostDiagnostics(map[string]any{"reason": make(chan int)}, maxHostDiagnosticBytes); got != nil {
		t.Fatalf("unencodable data accepted: %#v", got)
	}
}
