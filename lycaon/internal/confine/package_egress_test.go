package confine

import (
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

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

func TestOfflineVerificationDeniesExternalEndpointsImmediately(t *testing.T) {
	SetEgressRuleEvaluator(nil)
	SetEgressPosture(PostureObserve)
	t.Cleanup(func() {
		SetEgressRuleEvaluator(nil)
		SetEgressPosture(PostureObserve)
	})
	cmd := EgressCommand{
		SessionID:           "session",
		OfflineVerification: true,
	}
	ep := egressproxy.Endpoint{Host: "example.com", Port: 443, Transport: egressproxy.TransportHTTPConnect}
	allow, decided, _, _ := egressBroker.ruleOrPosture(t.Context(), cmd, ep, "test")
	if allow || !decided {
		t.Fatalf("offline verification decision = allow %v decided %v, want allow=false decided=true", allow, decided)
	}
}
