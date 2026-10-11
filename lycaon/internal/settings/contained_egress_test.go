package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

// TestContainedEgressAutoApprovesNetwork verifies containment-based approval.
func TestContainedEgressAutoApprovesNetwork(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "load approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := t.TempDir()

	for _, egress := range []string{hitl.ContainedEgressProxy, hitl.ContainedEgressDeny} {
		contained := hitl.Contained{FSJailed: true, Egress: egress, Roots: []string{proj}}
		for _, cmd := range []string{
			"git push origin main",
			"curl -X POST https://example.com/hook",
			"wget --post-data=x https://example.com",
			"ssh deploy@host",
			"scp a host:/tmp/",
		} {
			res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": cmd},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: contained,
},
})
			testutil.FailErr(t, "evaluate Contained egress", err)
			if !res.AutoApproved() || res.Required() {
				t.Fatalf("Contained.Egress=%s %q must auto-approve: %+v", egress, cmd, res)
			}
		}
	}
}

// TestStrictEgressAskComposesOnce verifies that the proxy raises the host ask.
func TestStrictEgressAskComposesOnce(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "load approval store", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	proj := t.TempDir()
	contained := hitl.Contained{FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{proj}}

	res, err := gate.Evaluate(context.Background(), hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "curl -s https://new-host.example/"},
},
Scope: hitl.ActionScope{
ProjectDir: proj,
},
Execution: hitl.ActionExecution{
Contained: contained,
},
})
	testutil.FailErr(t, "evaluate curl under Contained proxy", err)
	if !res.AutoApproved() || res.Required() {
		t.Fatalf("tool gate must not ask for Contained-proxy curl (proxy raises the Strict ask): %+v", res)
	}

	// Broker: Ask posture prompts once per host, then caches.
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })
	asks := 0
	confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
		asks++
		return ep.Host == "new-host.example"
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })

	cmd := confine.EgressCommand{SessionID: "compose-sess", ToolCallID: "tc-1"}
	if !confine.DecideAttributedHost(context.Background(), cmd, "new-host.example") {
		t.Fatal("first Strict host ask should allow")
	}
	if asks != 1 {
		t.Fatalf("first host must ask exactly once, got %d", asks)
	}
	asks = 0
	if !confine.DecideAttributedHost(context.Background(), cmd, "new-host.example") {
		t.Fatal("cached host must still allow")
	}
	if asks != 0 {
		t.Fatalf("cached host must not re-ask (no double-ask), got %d", asks)
	}
}
