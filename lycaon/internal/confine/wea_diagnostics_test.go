package confine

import (
	"testing"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

func TestObserveBrokerConnectionWEAHookAndZeroAllocWhenUnset(t *testing.T) {
	cmd := EgressCommand{
		SessionID:  "11111111-2222-3333-4444-555555555555",
		ProjectID:  "proj-1",
		ActionID:   "act-broker-1",
		ToolCallID: "call-broker-1",
	}
	ep := egressproxy.Endpoint{Host: "fixture.invalid", Port: 443}

	// 1. Unset: 0 allocs/op, returns false.
	restore := SetBrokerConnectionObserver(nil)
	defer restore()

	if observeBrokerConnectionWEA(cmd, ep) {
		t.Fatal("expected false when no broker connection observer is installed")
	}
	allocs := testing.AllocsPerRun(100, func() {
		_ = observeBrokerConnectionWEA(cmd, ep)
	})
	if allocs != 0 {
		t.Fatalf("unset observeBrokerConnectionWEA allocated %v allocs/op, want 0", allocs)
	}

	// 2. Installed: forwards exact attributed command and endpoint.
	var gotCmd EgressCommand
	var gotEP egressproxy.Endpoint
	calls := 0
	restoreHook := SetBrokerConnectionObserver(func(c EgressCommand, e egressproxy.Endpoint) {
		calls++
		gotCmd = c
		gotEP = e
	})
	defer restoreHook()

	if !observeBrokerConnectionWEA(cmd, ep) {
		t.Fatal("expected true when broker connection observer is installed")
	}
	if calls != 1 || gotCmd.SessionID != cmd.SessionID || gotCmd.ActionID != cmd.ActionID || gotEP.Host != ep.Host || gotEP.Port != ep.Port {
		t.Fatalf("unexpected forwarded event: calls=%d cmd=%+v ep=%+v", calls, gotCmd, gotEP)
	}
}
