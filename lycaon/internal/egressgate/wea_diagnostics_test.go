package egressgate

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

func TestObserveEgressDecisionWEAParityAndSupersession(t *testing.T) {
	cmd := confine.EgressCommand{
		SessionID:  "11111111-2222-3333-4444-555555555555",
		ProjectID:  "proj-1",
		ActionID:   "act-egress-1",
		ToolCallID: "call-egress-1",
	}

	// Disabled with production init() callbacks installed: 0 allocs/op (no host/port formatting!).
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	if observeEgressDecisionWEA(cmd, "fixture.invalid:443", true) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	ep := egressproxy.Endpoint{Host: "Fixture.Invalid", Port: 443}
	allocs := testing.AllocsPerRun(100, func() {
		brokerDecisionCallback(cmd, ep, true)
		brokerConnectionCallback(cmd, ep)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled installed broker callbacks allocated %v allocs/op, want 0", allocs)
	}

	// Enabled: refusal followed by later superseding admission emits 0 warnings.
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

	if !observeEgressDecisionWEA(cmd, "fixture.invalid:443", false) {
		t.Fatal("expected refusal observation to enqueue")
	}
	if !observeEgressDecisionWEA(cmd, "fixture.invalid:443", true) {
		t.Fatal("expected superseding admission observation to enqueue")
	}
	// Independent broker connection arrives after superseding admission -> 0 warnings!
	if !observeBrokerConnectionWEA(cmd, egressproxy.Endpoint{Host: "fixture.invalid", Port: 443}) {
		t.Fatal("expected broker connection after superseding admission to enqueue")
	}

	// Contradiction 1: broker observes connection to unrecorded destination -> contradiction.
	if !observeBrokerConnectionWEA(cmd, egressproxy.Endpoint{Host: "unrecorded.fixture.invalid", Port: 443}) {
		t.Fatal("expected unrecorded broker connection to enqueue")
	}

	// Contradiction 2: refusal followed by broker connection without superseding admission -> contradiction.
	refusedCmd := cmd
	refusedCmd.ActionID = "act-egress-refused"
	if !observeEgressDecisionWEA(refusedCmd, "refused.fixture.invalid:443", false) {
		t.Fatal("expected refusal decision to enqueue")
	}
	if !observeBrokerConnectionWEA(refusedCmd, egressproxy.Endpoint{Host: "refused.fixture.invalid", Port: 443}) {
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
