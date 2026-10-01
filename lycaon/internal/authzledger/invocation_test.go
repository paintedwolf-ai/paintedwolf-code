package authzledger

import "testing"

func TestInvocationCorrelationStaysWithinTheTask(t *testing.T) {
	ctx := WithInvocation(t.Context(), "worker", "root", "call-1")
	for _, session := range []string{"worker", "root"} {
		if InvocationToolCall(ctx, session) != "call-1" {
			t.Fatal("lost originating invocation")
		}
	}
	if InvocationToolCall(ctx, "other-task") != "" || InvocationToolCall(ctx, "") != "" {
		t.Fatal("attributed an unrelated session to this invocation")
	}
}
