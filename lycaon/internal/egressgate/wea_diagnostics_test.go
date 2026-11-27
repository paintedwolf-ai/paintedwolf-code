package egressgate

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

func TestObserveEgressDecisionWEAParityAndSupersession(t *testing.T) {
	cmd := confine.EgressCommand{
		SessionID:  "11111111-2222-3333-4444-555555555555",
		ProjectID:  "proj-1",
		ActionID:   "act-egress-1",
		ToolCallID: "call-egress-1",
	}

	// Disabled: 0 allocs/op.
	restoreOff := cmddiag.SetObserver(cmddiag.NewObserver(cmddiag.Config{Enabled: false}))
	if observeEgressDecisionWEA(cmd, "fixture.invalid:443", true) {
		restoreOff()
		t.Fatal("expected false when diagnostics disabled")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = observeEgressDecisionWEA(cmd, "fixture.invalid:443", true)
	})
	restoreOff()
	if allocs != 0 {
		t.Fatalf("disabled observeEgressDecisionWEA allocated %v allocs/op, want 0", allocs)
	}

	// Enabled: refusal followed by later superseding admission emits 0 warnings.
	var mu sync.Mutex
	var findings []cmddiag.Finding
	obs := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 8,
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
	obs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(findings) != 0 {
		t.Fatalf("expected 0 warnings on refusal + superseding admission, got %+v", findings)
	}
}
