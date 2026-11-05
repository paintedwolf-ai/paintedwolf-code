package toolfeedback

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestArgumentRecoveryDoesNotChangeTheRequestedOperation(t *testing.T) {
	bp := stockBlockPlane(t)
	ctx := tools.WithRecoveryTools(t.Context(), []string{"read", "edit", "write"})
	for _, rejected := range []*toolrejection.ToolReject{
		{Code: "EDIT_OLD_STRING_NOT_FOUND", Data: map[string]any{"path": "app.py", "old_len": 40, "basis_lines": 12}},
		{Code: "TOOL_ARGS_INVALID", Data: map[string]any{
			"suggested_tool": "write", "reason": "missing old_string", "argument_schema_tool": "edit",
			"missing_required_arguments": `{"missing_required":["old_string"],"expected_types":{"old_string":"string"},"received_keys":["new_string","path"]}`,
		}},
	} {
		err := bp.RejectObservation(ctx, "edit", "implement", nil, rejected)
		refusal, ok := guidance.RefusalFromError(err)
		if !ok || refusal.Code() != rejected.Code {
			t.Fatalf("expected %s, got %v", rejected.Code, err)
		}
		if rejected.Code == "TOOL_ARGS_INVALID" {
			copy := refusal.Copy["fix"] + refusal.Copy["instead"]
			if !strings.Contains(copy, "old_string") {
				t.Fatalf("schema recovery lost its required argument: %s", copy)
			}
			if strings.Contains(copy, "`write`") {
				t.Fatalf("schema recovery changed operations: %s", copy)
			}
		}
	}
}
