package confine

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

func TestAttributedEgressCurrentActionDoesNotCoverSiblingChild(t *testing.T) {
	SetEgressPosture(PostureAsk)
	defer SetEgressPosture(PostureObserve)
	asks := 0
	SetEgressResolver(func(_ context.Context, cmd EgressCommand, _ egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		asks++
		if cmd.ToolCallID != "actual-model-call" {
			t.Fatal("lost initiating model call")
		}
		return true
	})
	defer SetEgressResolver(nil)
	cmd := EgressCommand{SessionID: "child-session", ToolCallID: "actual-model-call", ActionID: "first-child"}
	defer ForgetEgressAction(cmd.SessionID, cmd.ActionID)
	if !DecideAttributedHost(t.Context(), cmd, "fixture.invalid") || !DecideAttributedHost(t.Context(), cmd, "fixture.invalid") || asks != 1 {
		t.Fatalf("first child current-action verdict was not retained: asks%d", asks)
	}
	cmd.ActionID = "second-child"
	defer ForgetEgressAction(cmd.SessionID, cmd.ActionID)
	if !DecideAttributedHost(t.Context(), cmd, "fixture.invalid") || asks != 2 {
		t.Fatalf("current-action verdict crossed native children: asks%d", asks)
	}
}
