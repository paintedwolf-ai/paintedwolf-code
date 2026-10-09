package toolexecution

import (
	"context"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// decisionGate returns one fixed decision; other gate methods are unused here.
type decisionGate struct {
	hitl.ApprovalGate
	decision *gate.Decision
}

func (g decisionGate) Evaluate(_ context.Context, _ hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	return &hitl.ApprovalResult{Decision: g.decision}, nil
}

func (decisionGate) GrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}

func (decisionGate) AbsorbedGrantOffers(hitl.ProposedAction, *hitl.ApprovalResult) []hitl.ApprovalGrantOffer {
	return nil
}

// chatLeaseDirectIPRuntime holds a live chat lease for every direct-IP request.
type chatLeaseDirectIPRuntime struct {
	memoryDirectIPRuntime
	leaseChecks int
}

func (r *chatLeaseDirectIPRuntime) LeaseCovers(string, hitl.DirectIPLease) bool {
	r.leaseChecks++
	return true
}

// A chat direct-IP lease settles the direct-network reason only. Settling skips
// the policy stage, so a detection or user rule on the same action must ask.
func TestDirectIPChatLeaseSettlesOnlyTheDirectNetworkReason(t *testing.T) {
	directFact := gate.Fact{Gate: api.GateUnobservedChannel, Key: "boundary.egress", Value: gate.EgressDirectIP, Source: "confine"}
	cases := []struct {
		name     string
		decision *gate.Decision
		settles  bool
	}{
		{"direct network alone", &gate.Decision{Primary: api.GateUnobservedChannel, Cited: []gate.Fact{directFact}}, true},
		{"with a detection", &gate.Decision{
			Primary: api.GateAuthorityMisuse, Also: []api.ApprovalGate{api.GateUnobservedChannel},
			Cited: []gate.Fact{{Gate: api.GateAuthorityMisuse, Key: "detection.rule", Value: "publish", Source: "detection_pack"}, directFact},
		}, false},
		{"with a user ask rule", &gate.Decision{
			Primary: api.GateUserRule, Also: []api.ApprovalGate{api.GateUnobservedChannel},
			Cited: []gate.Fact{{Gate: api.GateUserRule, Key: "rule.pattern", Value: "nc *", Source: "approval_rules"}, directFact},
		}, false},
	}
	contract, ok := toolcontract.Lookup("command")
	if !ok {
		t.Fatal("command contract missing")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := shortTempDir(t)
			runtime := &chatLeaseDirectIPRuntime{}
			executor := NewExecutor(nil, nil, "implement")
			executor.Approvals.approvalGate = decisionGate{decision: tc.decision}
			executor.Capabilities.SetDirectIPCapabilityRuntime(runtime)
			toolCtx := tools.ToolContext{
				SessionID: "chat", ToolCallID: "call-1",
				Roots:        []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
				ActiveRootID: "root",
				Invocation:   tools.Invocation{Contract: contract},
			}
			args := map[string]any{"command": "ntpdate time.example", "capability_request": map[string]any{"direct_ip": true}}
			result, err := executor.Capabilities.preflightDirectIPCapability(t.Context(), "command", args, toolCtx, false)
			settled := err == nil && result != nil && result.Authorized && runtime.leaseChecks > 0
			if settled != tc.settles {
				t.Fatalf("lease settled = %v, want %v (err %v, lease checks %d)", settled, tc.settles, err, runtime.leaseChecks)
			}
			if !tc.settles && runtime.leaseChecks != 0 {
				t.Fatalf("lease consulted for a decision it cannot answer: %d checks", runtime.leaseChecks)
			}
		})
	}
}
