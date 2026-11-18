package confine

import (
	"context"
	"net/netip"
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

// Below Strict the confinement leaves a dial quiet only when a retrieval
// capability reaches a source. Every dial whose address the agent wrote is the
// gate's to decide, whichever tool makes it.
func TestAgentAuthoredAddressesReachTheGate(t *testing.T) {
	ep := egressproxy.Endpoint{Host: "page.example", Transport: egressproxy.TransportHTTPRequest, Port: 443}
	for name, tc := range map[string]struct {
		cmd         EgressCommand
		reachesGate bool
	}{
		"source reader, source address":   {cmd: EgressCommand{Image: "fetch_url"}},
		"web search":                      {cmd: EgressCommand{Image: "web_search"}},
		"MCP tool":                        {cmd: EgressCommand{ToolName: "mcp_acme_ship"}},
		"source reader, displayed link":   {cmd: EgressCommand{Image: "fetch_url", Origin: AddressDisplayed}, reachesGate: true},
		"source reader, authored address": {cmd: EgressCommand{Image: "fetch_url", Origin: AddressAuthored}, reachesGate: true},
		"request tool":                    {cmd: EgressCommand{Image: "http_request", ToolName: "http_request"}, reachesGate: true},
		"command":                         {cmd: EgressCommand{ToolName: "command", CommandLine: "curl https://page.example/"}, reachesGate: true},
	} {
		t.Run(name, func(t *testing.T) {
			b := newTestBroker()
			reached := false
			b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
				reached = true
				return true
			}
			cmd := tc.cmd
			cmd.SessionID, cmd.ToolCallID = "s1", "a1"
			if !b.decideAttributedEndpoint(context.Background(), cmd, ep) {
				t.Fatal("the dial was refused")
			}
			if reached != tc.reachesGate {
				t.Fatalf("reached the gate = %v, want %v", reached, tc.reachesGate)
			}
		})
	}
}

// A host the agent named in a retrieval request, or one beneath it, reaches
// the gate; the request's other hosts stay its sources.
func TestNamedHostsReachTheGate(t *testing.T) {
	cmd := EgressCommand{SessionID: "s1", Image: "web_search", NamedHosts: []string{"named.example"}}
	for host, authored := range map[string]bool{
		"named.example":       true,
		"Named.Example":       true,
		"www.named.example":   true,
		"notnamed.example":    false,
		"example":             false,
		"named.example.other": false,
		"result.example":      false,
	} {
		t.Run(host, func(t *testing.T) {
			if got := cmd.OriginOf(host) == AddressAuthored; got != authored {
				t.Fatalf("authored = %v, want %v", got, authored)
			}
			b := newTestBroker()
			reached := false
			b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
				reached = true
				return true
			}
			dial := cmd
			dial.ToolCallID = "call-" + host
			ep := egressproxy.Endpoint{Host: host, Transport: egressproxy.TransportHTTPRequest, Port: 443}
			if !b.decideAttributedEndpoint(context.Background(), dial, ep) {
				t.Fatal("the dial was refused")
			}
			if reached != authored {
				t.Fatalf("reached the gate = %v, want %v", reached, authored)
			}
		})
	}
}

func TestReducedPackageExecutionAllowsOnlyReviewedRegistryHosts(t *testing.T) {
	SetEgressRuleEvaluator(nil)
	SetEgressPosture(PostureObserve)
	t.Cleanup(func() {
		SetEgressRuleEvaluator(nil)
		SetEgressPosture(PostureObserve)
	})
	cmd := EgressCommand{
		SessionID: "session", DeclaredHosts: []string{"registry.npmjs.org"},
		ReducedPackageExecution: true,
	}
	registry := egressproxy.Endpoint{Host: "registry.npmjs.org", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	if allow, decided, _, _ := egressBroker.ruleOrPosture(t.Context(), cmd, registry, "test"); !allow || !decided {
		t.Fatalf("reviewed registry decision = allow %v decided %v", allow, decided)
	}
	unknown := egressproxy.Endpoint{Host: "collect.example", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	if allow, decided, _, _ := egressBroker.ruleOrPosture(t.Context(), cmd, unknown, "test"); allow || !decided {
		t.Fatalf("unreviewed host decision = allow %v decided %v", allow, decided)
	}
}

func TestOwnedLoopbackObservationRequiresNarrowedActionAdmission(t *testing.T) {
	endpoint := netip.MustParseAddrPort("127.0.0.1:8123")
	observed := 0
	registration := &ownedLoopbackRegistration{observe: func(context.Context, EgressCommand, netip.AddrPort) (string, bool) {
		observed++
		return "actual-resource", true
	}}
	broker := newTestBroker()
	broker.ownedLoopback = registration
	broker.tokens["ordinary"] = EgressCommand{SessionID: "session"}
	broker.tokens["conditional"] = EgressCommand{SessionID: "session", OwnedLoopbackPorts: []uint16{8123}}
	if _, ok := broker.ownedLoopbackFor(t.Context(), "ordinary", endpoint); ok || observed != 0 {
		t.Fatal("listener observation supplied missing action admission")
	}
	if id, ok := broker.ownedLoopbackFor(t.Context(), "conditional", endpoint); !ok || id != "actual-resource" || observed != 1 {
		t.Fatal("narrow conditional observation was not checked")
	}
	if _, ok := broker.ownedLoopbackFor(t.Context(), "conditional", netip.MustParseAddrPort("127.0.0.1:8124")); ok || observed != 1 {
		t.Fatal("conditional admission widened ports")
	}
	delete(broker.tokens, "conditional")
	if _, ok := broker.ownedLoopbackFor(t.Context(), "conditional", endpoint); ok {
		t.Fatal("released action retained authority")
	}
}

// Disabled approvals stand down ask-only checks but never an authored deny:
// a denied host stays decided-denied while an unruled host is allowed.
func TestEgressDenyRuleHoldsWhenApprovalsAreDisabled(t *testing.T) {
	SetApprovalsDisabledSource(func(EgressCommand) bool { return true })
	t.Cleanup(func() { SetApprovalsDisabledSource(nil) })
	withRule(t, EgressRuleDeny, "denied.example")
	cmd := EgressCommand{SessionID: "session"}
	b := newTestBroker()

	denied := egressproxy.Endpoint{Host: "denied.example", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	if allow, decided, _, _ := b.ruleOrPosture(t.Context(), cmd, denied, "test"); allow || !decided {
		t.Fatalf("deny rule under disabled approvals = allow %v decided %v, want a decided deny", allow, decided)
	}
	other := egressproxy.Endpoint{Host: "other.example", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	if allow, decided, _, _ := b.ruleOrPosture(t.Context(), cmd, other, "test"); !allow || !decided {
		t.Fatalf("unruled host under disabled approvals = allow %v decided %v, want a decided allow", allow, decided)
	}
}
