package confine

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

func withRule(t *testing.T, effect EgressRuleEffect, host string) {
	t.Helper()
	release := SetEgressRuleEvaluator(func(_ context.Context, _ EgressCommand, h string) EgressRuleResult {
		if h == host {
			return EgressRuleResult{Effect: effect, Pattern: host}
		}
		return EgressRuleResult{}
	})
	t.Cleanup(release)
}

// Every dial nothing decided is the gate's, under every confinement posture
// and whatever the chat has read: a fresh chat is not exempt.
func TestUndecidedDialsReachTheGate(t *testing.T) {
	for _, posture := range []EgressPosture{PostureObserve, PostureAsk} {
		SetEgressPosture(posture)
		t.Cleanup(func() { SetEgressPosture(PostureObserve) })
		b := newTestBroker()
		var reached []string
		b.resolver = func(_ context.Context, _ EgressCommand, ep egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
			reached = append(reached, ep.Host)
			return true
		}
		cmd := EgressCommand{SessionID: "s1", ToolCallID: "a1", DeclaredHosts: []string{"declared.example"}}
		for _, host := range []string{"paste.example", "declared.example"} {
			ep := egressproxy.Endpoint{Host: host, Transport: egressproxy.TransportHTTPRequest, Port: 443}
			if !b.decideAttributedEndpoint(context.Background(), cmd, ep) {
				t.Fatalf("%v: the gate allowed %s, so the dial proceeds", posture, host)
			}
		}
		if len(reached) != 2 {
			t.Fatalf("%v: gate saw %v, want both destinations", posture, reached)
		}
	}
}

// An authored deny rule refuses before review.
func TestAuthoredDenyDecidesWithoutTheGate(t *testing.T) {
	withRule(t, EgressRuleDeny, "ruled.example")
	b := newTestBroker()
	b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		t.Fatal("an authored deny rule reached the gate")
		return true
	}
	ep := egressproxy.Endpoint{Host: "ruled.example", Transport: egressproxy.TransportHTTPRequest, Port: 443}
	if b.decideAttributedEndpoint(context.Background(), EgressCommand{SessionID: "s1", ToolCallID: "a1"}, ep) {
		t.Fatal("deny rule allowed the dial")
	}
}

// An ask rule hands the dial to the gate with its citation.
func TestAuthoredAskRuleReachesTheGateWithItsCitation(t *testing.T) {
	withRule(t, EgressRuleAsk, "asked.example")
	b := newTestBroker()
	var cited *EgressUserRule
	b.resolver = func(_ context.Context, cmd EgressCommand, _ egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		cited = cmd.UserRule
		return false
	}
	ep := egressproxy.Endpoint{Host: "asked.example", Transport: egressproxy.TransportHTTPRequest, Port: 443}
	if b.decideAttributedEndpoint(context.Background(), EgressCommand{SessionID: "s1", ToolCallID: "a1"}, ep) {
		t.Fatal("the gate refused, so the dial must not proceed")
	}
	if cited == nil || cited.Pattern != "asked.example" {
		t.Fatalf("gate saw rule %+v", cited)
	}
}

// A retrieval capability's dial is decided as the gate would decide it: below
// Strict quietly unless a rule asks; at Strict it reaches the gate, which
// confirms a first contact.
func TestRetrievalDialsFollowTheirContract(t *testing.T) {
	b := newTestBroker()
	reached := false
	b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		reached = true
		return false
	}
	cmd := EgressCommand{SessionID: "s1", ToolCallID: "a1", ToolName: "fetch_url"}
	ep := egressproxy.Endpoint{Host: "page.example", Transport: egressproxy.TransportHTTPRequest, Port: 443}
	if !b.decideAttributedEndpoint(context.Background(), cmd, ep) || reached {
		t.Fatalf("a retrieval dial asked: reached %v", reached)
	}
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	strict := cmd
	strict.ToolCallID = "a2"
	if b.decideAttributedEndpoint(context.Background(), strict, ep) || !reached {
		t.Fatal("a retrieval dial at Strict did not reach the gate")
	}
	SetEgressPosture(PostureObserve)
	reached = false
	asked := cmd
	asked.ToolCallID = "a3"
	withRule(t, EgressRuleAsk, "page.example")
	if b.decideAttributedEndpoint(context.Background(), asked, ep) || !reached {
		t.Fatal("an ask rule on a retrieval dial did not reach the gate")
	}
}
