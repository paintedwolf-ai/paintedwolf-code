package confine

import (
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

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
