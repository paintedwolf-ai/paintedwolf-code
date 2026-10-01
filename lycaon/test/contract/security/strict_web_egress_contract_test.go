package contract_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/gate"
)

func TestWebContentHostFollowsEffectivePosture(t *testing.T) {
	cases := []struct {
		posture gate.Posture
		wantAsk bool
	}{
		{posture: gate.PostureLight, wantAsk: false},
		{posture: gate.PostureBalanced, wantAsk: false},
		{posture: gate.PostureStrict, wantAsk: true},
	}
	for _, tc := range cases {
		t.Run(string(tc.posture), func(t *testing.T) {
			var asks atomic.Int32
			confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
				asks.Add(1)
				return true
			})
			t.Cleanup(func() { confine.SetEgressResolver(nil) })
			confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture {
				if tc.posture == gate.PostureStrict {
					return confine.PostureAsk
				}
				return confine.PostureObserve
			})
			t.Cleanup(func() { confine.SetEgressPostureResolver(nil) })

			ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{
				SessionID: "contract-content-" + string(tc.posture),
			})
			if err := egressgate.AwaitHost(ctx, "content.contract.test"); err != nil {
				t.Fatalf("await: %v", err)
			}
			if got := asks.Load(); got != boolCount(tc.wantAsk) {
				t.Fatalf("asks = %d, want %d", got, boolCount(tc.wantAsk))
			}
		})
	}
}

// Declared endpoints skip the ordinary Strict ask; explicit policy applies.
func TestStrictConfiguredProviderIsSilentButPolicyAskStillHolds(t *testing.T) {
	var asks atomic.Int32
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		asks.Add(1)
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture {
		return confine.PostureAsk
	})
	t.Cleanup(func() { confine.SetEgressPostureResolver(nil) })
	confine.SetEgressRuleEvaluator(func(_ context.Context, _ confine.EgressCommand, host string) confine.EgressRuleResult {
		if host == "policy-asked.contract.test" {
			return confine.EgressRuleResult{Effect: confine.EgressRuleAsk, Pattern: host}
		}
		return confine.EgressRuleResult{}
	})
	t.Cleanup(func() { confine.SetEgressRuleEvaluator(nil) })

	ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{
		SessionID: "contract-provider",
		DeclaredHosts: []string{
			"provider.contract.test",
			"policy-asked.contract.test",
		},
	})
	if err := egressgate.AwaitHost(ctx, "provider.contract.test"); err != nil {
		t.Fatalf("configured provider: %v", err)
	}
	if asks.Load() != 0 {
		t.Fatalf("declared endpoint triggered %d ordinary Strict asks", asks.Load())
	}
	if err := egressgate.AwaitHost(ctx, "policy-asked.contract.test"); err != nil {
		t.Fatalf("policy-asked provider: %v", err)
	}
	if asks.Load() != 1 {
		t.Fatalf("explicit policy ask count = %d, want 1", asks.Load())
	}
}

// Undeclared provider destinations require the Strict ask.
func TestUndeclaredProviderHostStillAsksUnderStrict(t *testing.T) {
	var asks atomic.Int32
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		asks.Add(1)
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	confine.SetEgressPostureResolver(func(confine.EgressCommand) confine.EgressPosture {
		return confine.PostureAsk
	})
	t.Cleanup(func() { confine.SetEgressPostureResolver(nil) })

	ctx := egressgate.WithAttribution(context.Background(), confine.EgressCommand{
		SessionID:     "contract-undeclared",
		DeclaredHosts: []string{"declared.contract.test"},
	})
	if err := egressgate.AwaitHost(ctx, "redirected.contract.test"); err != nil {
		t.Fatalf("undeclared host: %v", err)
	}
	if asks.Load() != 1 {
		t.Fatalf("undeclared host ask count = %d, want 1", asks.Load())
	}
}

func boolCount(v bool) int32 {
	if v {
		return 1
	}
	return 0
}
