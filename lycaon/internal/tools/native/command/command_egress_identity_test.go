package command

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
)

// A verification check follows the session's network posture like any command.
func TestVerificationCheckLeavesEgressIdentityUnchanged(t *testing.T) {
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "session",
			ToolCallID: "call"},
	}
	plain := commandEgressIdentity(tctx, "command", "bun llmchat.ts")
	tctx.Execution.VerificationCheck = true
	check := commandEgressIdentity(tctx, "command", "bun llmchat.ts")
	if !reflect.DeepEqual(plain, check) {
		t.Fatalf("verification egress identity = %+v, want %+v", check, plain)
	}
}
