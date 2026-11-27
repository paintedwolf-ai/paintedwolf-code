package confine

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

func TestObserveBrokerConnectionWEACrossOwnerJoinAndDisabledZeroAlloc(t *testing.T) {
	cmd := EgressCommand{
		SessionID:  "11111111-2222-3333-4444-555555555555",
		ProjectID:  "proj-1",
		ActionID:   "act-broker-1",
		ToolCallID: "call-broker-1",
	}
	ep := egressproxy.Endpoint{Host: "fixture.invalid", Port: 443}

	// 1. Disabled: 0 allocs/op.
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	if observeBrokerConnectionWEA(cmd, ep) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = observeBrokerConnectionWEA(cmd, ep)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled observeBrokerConnectionWEA allocated %v allocs/op, want 0", allocs)
	}

	// 2. Enabled: verify cross-owner correlation through cmddiag.Observer.
	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 16,
		Now:           func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			findings = append(findings, f)
			mu.Unlock()
		},
	})
	restoreOn := cmddiag.SetObserver(obs)
	defer restoreOn()

	// 2a. Healthy: independent admitting decision precedes broker connection -> 0 findings.
	admitSeq := cmddiag.NextWEASeq()
	cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID:    cmd.SessionID,
		ProjectID:    cmd.ProjectID,
		InvocationID: cmd.ActionID,
		ScopeKey:     "dec-allow-1",
		Settled:      true,
		EgressDecisions: []cmddiag.WEAEgressDecisionFact{
			{
				DecisionID:     "dec-allow-1",
				SessionID:      cmd.SessionID,
				ProjectID:      cmd.ProjectID,
				InvocationID:   cmd.ActionID,
				DestinationRef: "fixture.invalid:443",
				GrantScope:     "exact_action",
				MaxUses:        1,
				Admitted:       true,
				DecisionSeq:    admitSeq,
			},
		},
	})
	if !observeBrokerConnectionWEA(cmd, ep) {
		t.Fatal("expected admitted broker connection to enqueue")
	}

	// 2b. Contradiction: broker observes connection to unrecorded destination -> contradiction.
	unrecordedEP := egressproxy.Endpoint{Host: "unrecorded.fixture.invalid", Port: 443}
	if !observeBrokerConnectionWEA(cmd, unrecordedEP) {
		t.Fatal("expected unrecorded broker connection to enqueue")
	}

	// 2c. Contradiction: independent refusal precedes broker connection -> contradiction.
	refusedEP := egressproxy.Endpoint{Host: "refused.fixture.invalid", Port: 443}
	refusedCmd := cmd
	refusedCmd.ActionID = "act-broker-refused"
	refuseSeq := cmddiag.NextWEASeq()
	cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID:    refusedCmd.SessionID,
		ProjectID:    refusedCmd.ProjectID,
		InvocationID: refusedCmd.ActionID,
		ScopeKey:     "dec-refuse-1",
		Settled:      true,
		EgressDecisions: []cmddiag.WEAEgressDecisionFact{
			{
				DecisionID:     "dec-refuse-1",
				SessionID:      refusedCmd.SessionID,
				InvocationID:   refusedCmd.ActionID,
				DestinationRef: "refused.fixture.invalid:443",
				Refused:        true,
				RefusalCode:    "HOST_DENIED",
				DecisionSeq:    refuseSeq,
			},
		},
	})
	if !observeBrokerConnectionWEA(refusedCmd, refusedEP) {
		t.Fatal("expected post-refusal broker connection to enqueue")
	}
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(findings) != 2 {
		t.Fatalf("expected 2 contradictions (unrecorded + after_refusal), got %d: %+v", len(findings), findings)
	}
	reasons := map[string]bool{}
	for _, f := range findings {
		if f.RuleID != cmddiag.RuleWEAUnrecordedOrRefusedEgress || f.Disposition != cmddiag.DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		reasons[f.Reason] = true
	}
	if !reasons["egress_connection_unrecorded_by_product"] || !reasons["egress_connection_after_refusal"] {
		t.Fatalf("expected both unrecorded and after_refusal reasons, got %+v", findings)
	}
}
