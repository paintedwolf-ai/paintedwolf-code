package confine

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

// A decision the host takes for an action's endpoint is the decision its
// confined launch meets when it dials that endpoint: one ask per endpoint,
// a different transport or port asks on its own, and the decision ends with
// the action.
func TestConfinedLaunchMeetsItsActionsEndpointDecision(t *testing.T) {
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	b := newTestBroker()
	cmd := EgressCommand{SessionID: "s1", ToolCallID: "call", ActionID: "act"}
	b.tokens["lineage"] = cmd
	var asked []egressproxy.Endpoint
	b.resolver = func(_ context.Context, _ EgressCommand, ep egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		asked = append(asked, ep)
		return true
	}
	tunnel := egressproxy.Endpoint{Transport: egressproxy.TransportHTTPConnect, Host: "git.test", Port: 443}
	stream := egressproxy.Endpoint{Transport: egressproxy.TransportSocksTCP, Host: "git.test", Port: 22}

	if !b.decideAttributedEndpoint(context.Background(), cmd, tunnel) {
		t.Fatal("host admission of the tunnel was refused")
	}
	if !b.decideEndpoint(context.Background(), "lineage", tunnel) {
		t.Fatal("the launch's tunnel was refused after its action admitted it")
	}
	if len(asked) != 1 {
		t.Fatalf("one endpoint asked %d times: %v", len(asked), asked)
	}
	if !b.decideEndpoint(context.Background(), "lineage", stream) || len(asked) != 2 {
		t.Fatalf("a different transport must ask on its own: %v", asked)
	}

	b.mu.Lock()
	b.forgetActionLocked(cmd.SessionID, cmd.ActionID)
	b.mu.Unlock()
	if !b.decideEndpoint(context.Background(), "lineage", tunnel) || len(asked) != 3 {
		t.Fatalf("a forgotten action's decision still answered: %v", asked)
	}
}

// Another action's decision never answers for this launch.
func TestConfinedLaunchIgnoresOtherActionsDecisions(t *testing.T) {
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	b := newTestBroker()
	b.tokens["lineage"] = EgressCommand{SessionID: "s1", ActionID: "mine"}
	asks := 0
	b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		asks++
		return asks == 1
	}
	tunnel := egressproxy.Endpoint{Transport: egressproxy.TransportHTTPConnect, Host: "git.test", Port: 443}
	if !b.decideAttributedEndpoint(context.Background(), EgressCommand{SessionID: "s1", ActionID: "other"}, tunnel) {
		t.Fatal("other action's admission was refused")
	}
	if b.decideEndpoint(context.Background(), "lineage", tunnel) {
		t.Fatal("another action's approval answered this launch")
	}
	if asks != 2 {
		t.Fatalf("asks = %d, want 2", asks)
	}
}
