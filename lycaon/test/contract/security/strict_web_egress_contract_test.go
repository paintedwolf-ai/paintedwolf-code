package contract_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/pkg/api"
)

// preDialFacts are the facts the egress resolver gathers for one dial.
func preDialFacts(dest gate.Endpoint) gate.Facts {
	return gate.Facts{
		Stage: gate.StagePreDial,
		Ran: gate.ProducerContainment | gate.ProducerDestination | gate.ProducerLease |
			gate.ProducerRule | gate.ProducerDetection | gate.ProducerExposure | gate.ProducerIngestion,
		Containment: gate.Containment{FSJailed: true, Egress: gate.EgressProxy},
		Destination: &dest,
	}
}

// A web content host reached by a retrieval tool is quiet below Strict. At
// Strict the dial reaches the approval gate, which confirms the first contact
// and stays quiet for a host the retrieval already reached.
func TestWebContentHostFollowsEffectivePosture(t *testing.T) {
	cases := []struct {
		posture        gate.Posture
		reachesGate    bool
		confinePosture confine.EgressPosture
	}{
		{posture: gate.PostureLight, confinePosture: confine.PostureObserve},
		{posture: gate.PostureBalanced, confinePosture: confine.PostureObserve},
		{posture: gate.PostureStrict, reachesGate: true, confinePosture: confine.PostureAsk},
	}
	for _, tc := range cases {
		t.Run(string(tc.posture), func(t *testing.T) {
			var reached atomic.Int32
			confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
				reached.Add(1)
				return true
			})
			t.Cleanup(func() { confine.SetEgressResolver(nil) })
			confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture { return tc.confinePosture })
			t.Cleanup(func() { confine.SetEgressPostureResolver(nil) })

			ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{
				SessionID: "contract-content-" + string(tc.posture), ToolCallID: "fetch", ToolName: "fetch_url",
			})
			if err := egressgate.AwaitHost(ctx, "content.contract.test"); err != nil {
				t.Fatalf("await: %v", err)
			}
			if got := reached.Load() == 1; got != tc.reachesGate {
				t.Fatalf("reached gate %d times, want reaches=%v", reached.Load(), tc.reachesGate)
			}

			first := preDialFacts(gate.Endpoint{Host: "content.contract.test", Retrieval: true, FirstUseThisSession: true})
			verdict, decision := gate.Evaluate(first, tc.posture)
			if asks := verdict == gate.Ask; asks != tc.reachesGate || (asks && decision.Primary != api.GateFirstHost) {
				t.Fatalf("first contact: %v %+v", verdict, decision)
			}
			repeat := preDialFacts(gate.Endpoint{Host: "content.contract.test", Retrieval: true})
			if verdict, decision := gate.Evaluate(repeat, tc.posture); verdict == gate.Ask {
				t.Fatalf("a retrieved host asked again: %+v", decision)
			}
		})
	}
}

// Declared endpoints skip Strict's first-contact ask; an authored ask rule
// still reaches the gate and asks.
func TestStrictConfiguredProviderIsSilentButPolicyAskStillHolds(t *testing.T) {
	provider := preDialFacts(gate.Endpoint{Host: "provider.contract.test", Configured: true, Declared: true, FirstUseThisSession: true})
	if verdict, decision := gate.Evaluate(provider, gate.PostureStrict); verdict == gate.Ask {
		t.Fatalf("declared endpoint asked at Strict: %+v", decision)
	}
	asked := provider
	asked.UserRule = &gate.UserRule{Category: "host", Pattern: "policy-asked.contract.test", Subject: "policy-asked.contract.test"}
	if verdict, decision := gate.Evaluate(asked, gate.PostureStrict); verdict != gate.Ask || decision.Primary != api.GateUserRule {
		t.Fatalf("explicit policy ask: %v %+v", verdict, decision)
	}

	var cited atomic.Pointer[confine.EgressUserRule]
	confine.SetEgressResolver(func(_ context.Context, cmd confine.EgressCommand, _ egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		cited.Store(cmd.UserRule)
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture { return confine.PostureAsk })
	t.Cleanup(func() { confine.SetEgressPostureResolver(nil) })
	confine.SetEgressRuleEvaluator(func(_ context.Context, _ confine.EgressCommand, host string) confine.EgressRuleResult {
		if host == "policy-asked.contract.test" {
			return confine.EgressRuleResult{Effect: confine.EgressRuleAsk, Pattern: host}
		}
		return confine.EgressRuleResult{}
	})
	t.Cleanup(func() { confine.SetEgressRuleEvaluator(nil) })
	ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{
		SessionID: "contract-provider", ToolCallID: "search",
		DeclaredHosts: []string{"provider.contract.test", "policy-asked.contract.test"},
	})
	if err := egressgate.AwaitHost(ctx, "policy-asked.contract.test"); err != nil {
		t.Fatalf("policy-asked provider: %v", err)
	}
	if rule := cited.Load(); rule == nil || rule.Pattern != "policy-asked.contract.test" {
		t.Fatalf("the gate saw rule %+v", rule)
	}
}

// Undeclared provider destinations require the Strict ask.
func TestUndeclaredProviderHostStillAsksUnderStrict(t *testing.T) {
	redirected := preDialFacts(gate.Endpoint{Host: "redirected.contract.test", FirstUseThisSession: true})
	if verdict, decision := gate.Evaluate(redirected, gate.PostureStrict); verdict != gate.Ask || decision.Primary != api.GateFirstHost {
		t.Fatalf("undeclared host at Strict: %v %+v", verdict, decision)
	}
}
