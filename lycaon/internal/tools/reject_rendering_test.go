package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
)

// A reject no Decision claims still reaches the agent as a block, not a bare Code.
func TestRejectBeforeInvokeRendersBlock(t *testing.T) {
	exec := NewDefaultToolExecutor(nil, nil, "implement")
	rej := &ToolReject{
		Code: "WORKER_BRANCH_CLAIM_FAILED",
		Data: map[string]any{"tool": "write", "reason": "worker branch claim returned empty root"},
	}
	err := exec.rejectBeforeInvoke(context.Background(), "write", "implement", nil, rej)
	assertRenderedReject(t, err, "WORKER_BRANCH_CLAIM_FAILED", "worker branch claim returned empty root")
}

// Post-invoke observations settle through the same exit.
func TestSettleRejectRendersBlock(t *testing.T) {
	exec := NewDefaultToolExecutor(nil, nil, "implement")
	rej := &ToolReject{
		Code: "TOOL_OWNER_FAILED",
		Data: map[string]any{"reason": "owner exploded"},
	}
	err := exec.settleReject(context.Background(), "write", "implement", nil, rej)
	assertRenderedReject(t, err, "TOOL_OWNER_FAILED", "owner exploded")
}

// Asserts the block renders and the structured reject survives it.
func assertRenderedReject(t *testing.T, err error, code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a reject")
	}
	refusal, ok := guidance.RefusalFromError(err)
	if !ok {
		t.Fatalf("reject did not render as a refusal: %T %v", err, err)
	}
	if body := refusal.Body; !strings.Contains(body, "Rejected:") ||
		!strings.Contains(body, "Code: "+code) ||
		!strings.Contains(body, reason) {
		t.Fatalf("body missing block, code, or cause:\n%s", body)
	}
	tr := AsToolReject(err)
	if tr == nil {
		t.Fatal("AsToolReject lost the structured reject")
	}
	if tr.Code != code {
		t.Fatalf("reject code = %q, want %q", tr.Code, code)
	}
}
