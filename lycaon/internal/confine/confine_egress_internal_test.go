package confine

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
)

func newTestBroker() *egressBrokerT {
	return &egressBrokerT{
		waiters:    map[string][]chan bool{},
		tokens:     map[string]EgressCommand{},
		egress:     map[string][]EgressHost{},
		loopback:   map[string]func(uint16) bool{},
		lineages:   map[string]EgressCommand{},
		lineageSeq: map[string]uint64{},
	}
}

func decideHTTPConnect(b *egressBrokerT, ctx context.Context, token, host string) bool {
	ep, err := egressproxy.ParseHTTPEndpoint(host, egressproxy.TransportHTTPConnect)
	if err != nil {
		return false
	}
	return b.decideEndpoint(ctx, token, ep)
}

func TestBrokerDeniesAttributedDialWithoutResolver(t *testing.T) {
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1", ToolCallID: "tc-1"}
	if decideHTTPConnect(b, context.Background(), "tok", "example.com") {
		t.Fatal("missing approval wiring must not authorize an attributed dial")
	}
}

func TestBrokerAutoAllowsUnattributedCommand(t *testing.T) {
	b := newTestBroker()
	b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		return false
	}
	// Unattributed broker probes have no approval subject.
	if !decideHTTPConnect(b, context.Background(), "unknown", "example.com") {
		t.Fatal("an unattributed connection must auto-allow")
	}
}

func TestBrokerResolvesPerSessionAndCaches(t *testing.T) {
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	SetEgressPosture(PostureAsk) // the approval card only fires under Ask
	defer SetEgressPosture(PostureObserve)
	asks := 0
	b.resolver = func(_ context.Context, _ EgressCommand, ep egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		asks++
		return ep.Host == "ok.test" // approve only ok.test
	}
	if !decideHTTPConnect(b, context.Background(), "tok", "ok.test") {
		t.Fatal("ok.test should be allowed after the resolver approves")
	}
	if decideHTTPConnect(b, context.Background(), "tok", "evil.test") {
		t.Fatal("evil.test should be denied after the resolver rejects")
	}
	// Verdicts are cached per session: a second decision does not re-prompt.
	asks = 0
	if !decideHTTPConnect(b, context.Background(), "tok", "ok.test") {
		t.Fatal("cached ok.test should still allow")
	}
	if asks != 0 {
		t.Fatalf("cached host must not re-prompt, got %d asks", asks)
	}
}

func TestBrokerRecordsEgressPerCommand(t *testing.T) {
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool { return true }
	SetEgressRuleEvaluator(func(_ context.Context, _ EgressCommand, host string) EgressRuleResult {
		if host == "blocked.test" {
			return EgressRuleResult{Effect: EgressRuleDeny, Pattern: "blocked.test"}
		}
		return EgressRuleResult{}
	})
	defer SetEgressRuleEvaluator(nil)

	decideHTTPConnect(b, context.Background(), "tok", "github.com")   // auto-allow → recorded allowed
	decideHTTPConnect(b, context.Background(), "tok", "github.com")   // duplicate → not re-recorded
	decideHTTPConnect(b, context.Background(), "tok", "blocked.test") // rule-denied → recorded blocked
	decideHTTPConnect(b, context.Background(), "ghost", "x.test")     // unattributed → not recorded

	got := b.egress["tok"]
	if len(got) != 2 {
		t.Fatalf("expected 2 unique hosts for the command, got %+v", got)
	}
	if got[0].Host != "github.com" || !got[0].Allowed {
		t.Fatalf("github.com should be recorded allowed, got %+v", got[0])
	}
	if got[0].Port != 443 || got[0].Transport != string(egressproxy.TransportHTTPConnect) || got[0].Attempts != 2 {
		t.Fatalf("github.com endpoint accounting incomplete: %+v", got[0])
	}
	if got[1].Host != "blocked.test" || got[1].Allowed {
		t.Fatalf("blocked.test should be recorded blocked, got %+v", got[1])
	}
	if len(b.egress["ghost"]) != 0 {
		t.Fatal("an unattributed connection must not be recorded")
	}
}

func TestActionLeaseBindsOnceAndDrainsOnClose(t *testing.T) {
	restore := SetBrokerForTest(nil, egressproxy.Addrs{HTTP: "127.0.0.1:1", SOCKS: "127.0.0.1:2"})
	t.Cleanup(restore)
	c := &Confinement{Network: NetworkProxyOnly}
	lease, err := BindAction(c, EgressCommand{SessionID: "s", ProjectID: "p"})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { lease.Close(t.Context()) })
	if !EgressBound(c) {
		t.Fatal("lease must be live before launch")
	}
	if c.LineageID == "" {
		t.Fatal("a bound action must carry a lineage identity")
	}
	if _, err := BindAction(c, EgressCommand{SessionID: "other"}); err == nil {
		t.Fatal("one boundary bound to two actions")
	}

	other := &Confinement{Network: NetworkProxyOnly}
	otherLease, err := BindAction(other, EgressCommand{SessionID: "other", ProjectID: "p"})
	if err != nil {
		t.Fatalf("bind other action: %v", err)
	}
	t.Cleanup(func() { otherLease.Close(t.Context()) })
	if other.LineageID == c.LineageID {
		t.Fatal("two actions shared one lineage identity")
	}
	// One front door serves both; identity is the caller, not the address.
	if c.ProxyAddr != other.ProxyAddr || c.SocksProxyAddr != other.SocksProxyAddr {
		t.Fatalf("actions received different broker addresses: %q vs %q", c.ProxyAddr, other.ProxyAddr)
	}

	egressBroker.observeEndpoint(c.LineageID, egressproxy.Endpoint{
		Host: "registry.npmjs.org", Port: 443, Transport: egressproxy.TransportHTTPConnect,
	}, true)
	got := lease.Close(t.Context())
	if len(got) != 1 || got[0].Host != "registry.npmjs.org" {
		t.Fatalf("close should return the recorded hosts, got %+v", got)
	}
	if EgressBound(c) {
		t.Fatal("closed lease remained live")
	}
	if again := lease.Close(t.Context()); len(again) != 1 {
		t.Fatalf("idempotent close lost observations: %+v", again)
	}
}

func TestEndpointObservationsAreBoundedAndStillAggregateKnownEndpoint(t *testing.T) {
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	for i := range maxEgressEndpointsPerCommand + 1 {
		b.observeEndpoint("tok", egressproxy.Endpoint{
			Host:      fmt.Sprintf("host-%d.example", i),
			Port:      443,
			Transport: egressproxy.TransportHTTPConnect,
		}, true)
	}
	if got := len(b.egress["tok"]); got != maxEgressEndpointsPerCommand {
		t.Fatalf("observations=%d want %d", got, maxEgressEndpointsPerCommand)
	}
	b.observeEndpoint("tok", egressproxy.Endpoint{
		Host:      "host-0.example",
		Port:      443,
		Transport: egressproxy.TransportHTTPConnect,
	}, true)
	if b.egress["tok"][0].Attempts != 2 {
		t.Fatalf("known endpoint did not aggregate at cap: %+v", b.egress["tok"][0])
	}
}

func TestHTTPObservationsPreservePortTransportAndAttempts(t *testing.T) {
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	request := egressproxy.Endpoint{Host: "api.example", Port: 8080, Transport: egressproxy.TransportHTTPRequest}
	connect := egressproxy.Endpoint{Host: "api.example", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	b.observeEndpoint("tok", request, true)
	b.observeEndpoint("tok", request, true)
	b.observeEndpoint("tok", connect, false)
	got := b.egress["tok"]
	if len(got) != 2 {
		t.Fatalf("observations=%+v", got)
	}
	if got[0].Port != 8080 || got[0].Transport != string(egressproxy.TransportHTTPRequest) || got[0].Attempts != 2 {
		t.Fatalf("HTTP request observation=%+v", got[0])
	}
	if got[1].Port != 443 || got[1].Transport != string(egressproxy.TransportHTTPConnect) || got[1].Attempts != 1 {
		t.Fatalf("HTTP CONNECT observation=%+v", got[1])
	}
}

func TestHTTPObservationsKeepMethodAndPathDistinct(t *testing.T) {
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	health := egressproxy.Endpoint{
		Host: "api.example", Port: 80, Transport: egressproxy.TransportHTTPRequest,
		RequestMethod: http.MethodGet, RequestPath: "/health",
	}
	deploy := egressproxy.Endpoint{
		Host: "api.example", Port: 80, Transport: egressproxy.TransportHTTPRequest,
		RequestMethod: http.MethodPost, RequestPath: "/deploy",
	}
	b.observeEndpoint("tok", health, true)
	b.observeEndpoint("tok", deploy, true)
	b.reportHTTPOutcome("tok", deploy, http.StatusAccepted, nil)

	got := b.egress["tok"]
	if len(got) != 2 {
		t.Fatalf("observations = %+v", got)
	}
	if got[0].RequestMethod != http.MethodGet || got[0].RequestPath != "/health" || got[0].Outcome != "" {
		t.Fatalf("health observation = %+v", got[0])
	}
	if got[1].RequestMethod != http.MethodPost || got[1].RequestPath != "/deploy" ||
		got[1].ResponseStatus != http.StatusAccepted || got[1].Outcome != "response_received" {
		t.Fatalf("deploy observation = %+v", got[1])
	}
}

func TestDecideAttributedHostAsksAndCaches(t *testing.T) {
	SetEgressPosture(PostureAsk)
	defer SetEgressPosture(PostureObserve)
	asks := 0
	SetEgressResolver(func(_ context.Context, cmd EgressCommand, ep egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		asks++
		if cmd.SessionID != "web-sess" || ep.Host != "provider.test" {
			t.Fatalf("unexpected ask %+v %s", cmd, ep.Host)
		}
		return true
	})
	defer SetEgressResolver(nil)

	cmd := EgressCommand{SessionID: "web-sess", ToolCallID: "tc"}
	if !DecideAttributedHost(context.Background(), cmd, "provider.test") {
		t.Fatal("approve should allow")
	}
	asks = 0
	if !DecideAttributedHost(context.Background(), cmd, "provider.test") {
		t.Fatal("cached allow")
	}
	if asks != 0 {
		t.Fatalf("must not re-ask cached host, got %d", asks)
	}
	if !DecideAttributedHost(context.Background(), EgressCommand{SessionID: "web-sess", ToolCallID: "tc-2"}, "provider.test") {
		t.Fatal("second action should allow after its own approval")
	}
	if asks != 1 {
		t.Fatalf("current-action approval leaked across actions: asks=%d want 1", asks)
	}
}

func TestDecideAttributedHostRejectsMalformedEndpoint(t *testing.T) {
	cmd := EgressCommand{SessionID: "sess", ToolCallID: "call"}
	if DecideAttributedHost(context.Background(), cmd, "bad\n.example") {
		t.Fatal("malformed attributed endpoint must fail closed")
	}
}

func TestBrokerRuleEvaluatorSeparatesDenyAndAsk(t *testing.T) {
	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	var cited *EgressUserRule
	b.resolver = func(_ context.Context, cmd EgressCommand, _ egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		cited = cmd.UserRule
		return true
	}
	SetEgressRuleEvaluator(func(_ context.Context, _ EgressCommand, host string) EgressRuleResult {
		switch host {
		case "asked.test":
			return EgressRuleResult{Effect: EgressRuleAsk, Pattern: "asked.test"}
		case "blocked.test":
			return EgressRuleResult{Effect: EgressRuleDeny, Pattern: "blocked.test"}
		default:
			return EgressRuleResult{}
		}
	})
	defer SetEgressRuleEvaluator(nil)

	if !decideHTTPConnect(b, context.Background(), "tok", "asked.test") {
		t.Fatal("an ask policy should proceed after approval")
	}
	if cited == nil || cited.Pattern != "asked.test" || cited.Subject != "asked.test" {
		t.Fatalf("ask rule citation = %+v", cited)
	}
	if decideHTTPConnect(b, context.Background(), "tok", "blocked.test") {
		t.Fatal("a rule-denied host should deny without prompting")
	}
}

// stubEgressDetection reports a fixed citation.
type stubEgressDetection struct {
	match EgressDetectionCitation
	ok    bool
}

func (s stubEgressDetection) Match(EgressDetectionObservation) (EgressDetectionCitation, bool) {
	return s.match, s.ok
}

func (s stubEgressDetection) Escalates(match EgressDetectionCitation, posture gate.Posture) bool {
	switch posture {
	case "light":
		return match.Level == "critical"
	case "balanced":
		return match.Level == "high" || match.Level == "critical"
	case "strict":
		return match.Level == "medium" || match.Level == "high" || match.Level == "critical"
	default:
		return false
	}
}

func TestDetectionHoldsConnectUnderObserve(t *testing.T) {
	SetEgressPosture(PostureObserve)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	SetDetectionApprovalPosture(func(EgressCommand) gate.Posture { return gate.PostureBalanced })
	t.Cleanup(func() { SetDetectionApprovalPosture(nil) })
	SetEgressDetectionSource(stubEgressDetection{
		ok: true,
		match: EgressDetectionCitation{
			PackID: "egress-providers", RuleID: "r1", RuleTitle: "iam", Level: "critical",
		},
	})
	t.Cleanup(func() { SetEgressDetectionSource(nil) })

	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	var gotDet *EgressDetectionCitation
	b.resolver = func(_ context.Context, _ EgressCommand, ep egressproxy.Endpoint, det *EgressDetectionCitation) bool {
		gotDet = det
		return ep.Host == "iam.amazonaws.com"
	}
	if !decideHTTPConnect(b, context.Background(), "tok", "iam.amazonaws.com") {
		t.Fatal("allow verdict should open CONNECT")
	}
	if gotDet == nil || gotDet.Level != "critical" {
		t.Fatalf("detection=%+v", gotDet)
	}
}

func TestForgetActionRetainsOrdinaryHostVerdict(t *testing.T) {
	t.Parallel()
	b := newTestBroker()
	b.verdict.Store("host\x00s1\x00api.example", true)
	b.verdict.Store("detection\x00s1\x00action-1\x00api.example\x00p\x00r\x00high", true)
	b.verdict.Store("detection\x00s1\x00action-2\x00api.example\x00p\x00r\x00high", true)
	b.forgetActionLocked("s1", "action-1")
	if b.verdict.Len() != 2 {
		t.Fatalf("verdicts=%v", b.verdict.Snapshot())
	}
	host, hostOK := b.verdict.Load("host\x00s1\x00api.example")
	detection, detectionOK := b.verdict.Load("detection\x00s1\x00action-2\x00api.example\x00p\x00r\x00high")
	if !hostOK || !host || !detectionOK || !detection {
		t.Fatalf("unrelated verdict was removed: %v", b.verdict.Snapshot())
	}
}

func TestDetectionCannotSoftenHostDeny(t *testing.T) {
	SetEgressPosture(PostureObserve)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	SetDetectionApprovalPosture(func(EgressCommand) gate.Posture { return gate.PostureBalanced })
	t.Cleanup(func() { SetDetectionApprovalPosture(nil) })
	SetEgressDetectionSource(stubEgressDetection{
		ok: true, match: EgressDetectionCitation{PackID: "p", RuleID: "r", RuleTitle: "t", Level: "critical"},
	})
	t.Cleanup(func() { SetEgressDetectionSource(nil) })
	SetEgressRuleEvaluator(func(_ context.Context, _ EgressCommand, host string) EgressRuleResult {
		if host == "evil.example" {
			return EgressRuleResult{Effect: EgressRuleDeny, Pattern: "evil.example"}
		}
		return EgressRuleResult{}
	})
	t.Cleanup(func() { SetEgressRuleEvaluator(nil) })

	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	asked := 0
	b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		asked++
		return true
	}
	if decideHTTPConnect(b, context.Background(), "tok", "evil.example") {
		t.Fatal("host deny must win")
	}
	if asked != 0 {
		t.Fatalf("asked=%d", asked)
	}
}

func TestHostAskPolicyCannotSkipDetectionHold(t *testing.T) {
	SetEgressPosture(PostureObserve)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	SetDetectionApprovalPosture(func(EgressCommand) gate.Posture { return gate.PostureBalanced })
	t.Cleanup(func() { SetDetectionApprovalPosture(nil) })
	SetEgressDetectionSource(stubEgressDetection{
		ok: true, match: EgressDetectionCitation{PackID: "p", RuleID: "r", RuleTitle: "t", Level: "critical"},
	})
	t.Cleanup(func() { SetEgressDetectionSource(nil) })
	SetEgressRuleEvaluator(func(_ context.Context, _ EgressCommand, host string) EgressRuleResult {
		if host == "ok.example" {
			return EgressRuleResult{Effect: EgressRuleAsk, Pattern: "ok.example"}
		}
		return EgressRuleResult{}
	})
	t.Cleanup(func() { SetEgressRuleEvaluator(nil) })

	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1"}
	asked := 0
	b.resolver = func(_ context.Context, _ EgressCommand, ep egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		asked++
		return true
	}
	if !decideHTTPConnect(b, context.Background(), "tok", "ok.example") {
		t.Fatal("approved detection hold should proceed")
	}
	if asked != 1 {
		t.Fatalf("asked=%d", asked)
	}
}

func TestCachedHostApprovalCannotSilenceDetection(t *testing.T) {
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	SetDetectionApprovalPosture(func(EgressCommand) gate.Posture { return gate.PostureBalanced })
	t.Cleanup(func() { SetDetectionApprovalPosture(nil) })
	SetEgressDetectionSource(nil)
	t.Cleanup(func() { SetEgressDetectionSource(nil) })

	b := newTestBroker()
	b.tokens["ordinary"] = EgressCommand{SessionID: "s1", ToolCallID: "tc-1"}
	asked := 0
	b.resolver = func(_ context.Context, _ EgressCommand, ep egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		asked++
		return true
	}
	if !decideHTTPConnect(b, context.Background(), "ordinary", "registry.example") {
		t.Fatal("ordinary host approval should allow")
	}
	if asked != 1 {
		t.Fatalf("asked=%d", asked)
	}

	SetEgressDetectionSource(stubEgressDetection{
		ok: true, match: EgressDetectionCitation{PackID: "p", RuleID: "r", RuleTitle: "t", Level: "critical"},
	})
	b.tokens["detected"] = EgressCommand{SessionID: "s1", ToolCallID: "tc-2"}
	if !decideHTTPConnect(b, context.Background(), "detected", "registry.example") {
		t.Fatal("detection approval should allow its exact action")
	}
	if asked != 2 {
		t.Fatalf("detection must bypass cached host approval: asked=%d", asked)
	}
}

func TestCoalesceSameHostWaiters(t *testing.T) {
	SetEgressPosture(PostureObserve)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	SetDetectionApprovalPosture(func(EgressCommand) gate.Posture { return gate.PostureBalanced })
	t.Cleanup(func() { SetDetectionApprovalPosture(nil) })
	SetEgressDetectionSource(stubEgressDetection{
		ok: true, match: EgressDetectionCitation{PackID: "p", RuleID: "r", RuleTitle: "t", Level: "critical"},
	})
	t.Cleanup(func() { SetEgressDetectionSource(nil) })

	b := newTestBroker()
	b.tokens["tok"] = EgressCommand{SessionID: "s1", ToolCallID: "tc-1"}
	release := make(chan struct{})
	var asked atomic.Int32
	b.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		asked.Add(1)
		<-release
		return true
	}
	done := make(chan bool, 2)
	go func() { done <- decideHTTPConnect(b, context.Background(), "tok", "shared.example") }()
	// Wait until first parks.
	for {
		b.mu.Lock()
		key := egressVerdictKey(b.tokens["tok"], "tok", egressproxy.Endpoint{
			Host: "shared.example", Transport: egressproxy.TransportHTTPConnect, Port: 443,
		}, nil)
		_, parked := b.waiters[key]
		b.mu.Unlock()
		if parked || asked.Load() > 0 {
			break
		}
	}
	go func() { done <- decideHTTPConnect(b, context.Background(), "tok", "shared.example") }()
	close(release)
	a1 := <-done
	a2 := <-done
	if !a1 || !a2 {
		t.Fatal("both waiters should allow")
	}
	if asked.Load() != 1 {
		t.Fatalf("asked=%d want 1", asked.Load())
	}
	b.tokens["another-action"] = EgressCommand{SessionID: "s1", ToolCallID: "tc-2"}
	if !decideHTTPConnect(b, context.Background(), "another-action", "shared.example") {
		t.Fatal("another exact action should allow after its own approval")
	}
	if asked.Load() != 2 {
		t.Fatalf("another action must ask independently: asked=%d", asked.Load())
	}
}

// A destination fan-out shares one ask through its normalized host-set digest.
func TestDeclaredHostsDigestIsOrderAndCaseStable(t *testing.T) {
	t.Parallel()
	a, countA := DeclaredHostsDigest([]string{"b.example", "A.example", "b.example"})
	b, countB := DeclaredHostsDigest([]string{"a.example", "B.EXAMPLE"})
	if a == "" || a != b {
		t.Fatalf("digest must ignore order, case, and duplicates: %q vs %q", a, b)
	}
	if countA != 2 || countB != 2 {
		t.Fatalf("counts = %d/%d want 2 (deduped)", countA, countB)
	}
	if empty, n := DeclaredHostsDigest(nil); empty != "" || n != 0 {
		t.Fatal("an empty set has no identity")
	}
}

// A redirect that moves an ordinary request to another port on the same host is
// another destination. The verdict cache answers one endpoint, so the second port
// asks for itself rather than riding the first port's answer.
func TestHTTPRequestVerdictIsScopedToThePort(t *testing.T) {
	SetEgressPosture(PostureAsk)
	defer SetEgressPosture(PostureObserve)
	var asked []uint16
	SetEgressResolver(func(_ context.Context, _ EgressCommand, ep egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		asked = append(asked, ep.Port)
		return true
	})
	defer SetEgressResolver(nil)

	cmd := EgressCommand{SessionID: "redirect-sess", ToolCallID: "tc-redirect"}
	for _, hostport := range []string{"api.test:80", "api.test:8443", "api.test:80"} {
		if !DecideAttributedHTTPRequest(context.Background(), cmd, hostport, http.MethodGet, "/v1") {
			t.Fatalf("approved request to %s was refused", hostport)
		}
	}
	if len(asked) != 2 || asked[0] != 80 || asked[1] != 8443 {
		t.Fatalf("asked ports = %v, want one ask per port and a cache hit on the repeat", asked)
	}
}

func TestEgressPolicyReleasePreservesReplacementDeny(t *testing.T) {
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	oldRelease := SetEgressRuleEvaluator(func(context.Context, EgressCommand, string) EgressRuleResult {
		return EgressRuleResult{Effect: EgressRuleAsk, Pattern: "old-policy"}
	})
	currentRelease := SetEgressRuleEvaluator(func(context.Context, EgressCommand, string) EgressRuleResult {
		return EgressRuleResult{Effect: EgressRuleDeny, Pattern: "current-policy"}
	})
	defer oldRelease()
	defer currentRelease()
	oldRelease()
	oldRelease()
	broker := newTestBroker()
	broker.tokens["owner-token"] = EgressCommand{SessionID: "owner-session"}
	asked := false
	broker.resolver = func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		asked = true
		return true
	}
	if decideHTTPConnect(broker, t.Context(), "owner-token", "blocked.test") || asked {
		t.Fatal("releasing the old host bypassed the current authored deny")
	}
	currentRelease()
	if !decideHTTPConnect(broker, t.Context(), "owner-token", "after-release.test") || !asked {
		t.Fatal("released policy retained a stale authored deny")
	}
}

func TestEgressResolverReleasePreservesReplacementAndDrainsApproval(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	SetEgressPosture(PostureAsk)
	t.Cleanup(func() { SetEgressPosture(PostureObserve) })
	oldRelease := SetEgressResolver(func(context.Context, EgressCommand, egressproxy.Endpoint, *EgressDetectionCitation) bool {
		t.Error("released resolver received a dial")
		return false
	})
	entered, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	allowFinish := sync.OnceFunc(func() { close(finish) })
	defer allowFinish()
	currentRelease := SetEgressResolver(func(ctx context.Context, _ EgressCommand, _ egressproxy.Endpoint, _ *EgressDetectionCitation) bool {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-finish
		return true
	})
	t.Cleanup(func() {
		if err := currentRelease(context.Background()); err != nil {
			t.Errorf("cleanup approval resolver: %v", err)
		}
	})
	if err := oldRelease(ctx); err != nil {
		t.Fatalf("release old resolver: %v", err)
	}
	egressBroker.mu.Lock()
	copied := egressBroker.resolver
	egressBroker.mu.Unlock()
	dial := make(chan bool, 1)
	go func() {
		dial <- DecideAttributedHost(ctx, EgressCommand{SessionID: "resolver-owner"}, "approval.test")
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("approval did not enter current resolver")
	}
	drained := make(chan error, 1)
	go func() { drained <- currentRelease(ctx) }()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("release did not cancel active approval")
	}
	select {
	case err := <-drained:
		t.Fatalf("release returned before active approval finished: %v", err)
	default:
	}
	if copied(t.Context(), EgressCommand{}, egressproxy.Endpoint{}, nil) {
		t.Fatal("copied callback admitted while owner was draining")
	}
	allowFinish()
	if err := <-drained; err != nil {
		t.Fatalf("drain current approval: %v", err)
	}
	if <-dial {
		t.Fatal("canceled approval authorized its dial")
	}
	if copied(t.Context(), EgressCommand{}, egressproxy.Endpoint{}, nil) {
		t.Fatal("copied callback authorized after owner close")
	}
	if DecideAttributedHost(t.Context(), EgressCommand{SessionID: "closed-resolver"}, "after-close.test") {
		t.Fatal("closed resolver authorized a new dial")
	}
	if err := currentRelease(t.Context()); err != nil {
		t.Fatalf("repeat resolver release: %v", err)
	}
}
