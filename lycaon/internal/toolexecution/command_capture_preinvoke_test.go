package toolexecution_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestCommandCaptureValidationRejectsBeforeApprovalAndOwnerDispatch(t *testing.T) {
	boundary := sandbox.NewBoundary(sandbox.Config{}, []sandbox.ToolProfile{{
		ID:    "implement",
		Tools: map[string]bool{"command": true},
	}})
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register command", reg.Register("command", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		t.Fatal("invalid capture command must not reach owner dispatch")
		return "", nil
	}))
	gate := &countingApprovalGate{}
	policy := toolexecution.NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate)
	exec := toolexecution.NewExecutor(policy, reg, "implement")

	for _, tc := range []struct {
		name     string
		args     map[string]any
		wantCode string
	}{
		{
			name:     "terminal_capture pipeline",
			args:     map[string]any{"command": `printf "1\n4\n" | python3 src/main.py`, "terminal_capture": map[string]any{}},
			wantCode: "COMMAND_NOT_ARGV",
		},
		{
			name:     "terminal_capture sequence",
			args:     map[string]any{"command": "python3 a.py && python3 b.py", "terminal_capture": map[string]any{}},
			wantCode: "COMMAND_NOT_ARGV",
		},
		{
			name:     "terminal_capture redirect",
			args:     map[string]any{"command": "python3 src/main.py > out.txt", "terminal_capture": map[string]any{}},
			wantCode: "COMMAND_REDIRECTION_UNSUPPORTED",
		},
		{
			name:     "terminal_capture incompatible stdin",
			args:     map[string]any{"command": "python3 src/main.py", "stdin": "1\n", "terminal_capture": map[string]any{}},
			wantCode: "TOOL_ARGS_INVALID",
		},
		{
			name:     "snapshot_capture incompatible background",
			args:     map[string]any{"command": "python3 src/main.py", "background": true, "snapshot_capture": map[string]any{}},
			wantCode: "TOOL_ARGS_INVALID",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var effects tools.ToolInvocationOut
			_, err := exec.Invoke(t.Context(), "command", tc.args, tools.ToolContext{
				Identity: tools.InvocationIdentity{Agent: "implement"},
				Effects:  tools.InvocationEffects{Out: &effects},
			})
			if err == nil {
				t.Fatal("expected pre-invoke rejection")
			}
			refusal, ok := guidance.RefusalFromError(err)
			if !ok || refusal.Code() != tc.wantCode {
				t.Fatalf("refusal = %v (code=%q), want %q", err, refusal.Code(), tc.wantCode)
			}
			if effects.OwnerInvoked {
				t.Fatal("OwnerInvoked must remain false on pre-invoke capture rejection")
			}
			if got := gate.calls.Load(); got != 0 {
				t.Fatalf("approval gate calls = %d, want 0", got)
			}
		})
	}
}
