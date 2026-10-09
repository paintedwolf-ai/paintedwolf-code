package toolexecution

import (
	"github.com/lycaon/lycaon/internal/tools"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
)

func directIPLeaseFor(t *testing.T, args map[string]any, declared []string) hitl.DirectIPLease {
	t.Helper()
	tctx := tools.ToolContext{
		ProjectID:    "proj-1",
		SessionID:    "chat-a",
		Roots:        []projectroot.RootRef{{ID: "root-1", Path: "/tmp/proj", IsPrimary: true}},
		ActiveRootID: "root-1",
	}
	_, lease := DirectIPReview("command", args, tctx, declared, nil)
	return lease
}

// The direct-network card re-asks only when the command, its declared
// destinations, or the confinement changes; terminal capture and deadlines are neutral.
func TestDirectIPLeaseIdentitySurvivesNeutralArgs(t *testing.T) {
	t.Parallel()
	declared := []string{"https://open.er-api.com:443", "https://api.frankfurter.dev:443"}
	capReq := func(dests []string) map[string]any {
		list := make([]any, 0, len(dests))
		for _, d := range dests {
			list = append(list, d)
		}
		return map[string]any{"direct_ip": map[string]any{"declared_destinations": list}}
	}
	base := directIPLeaseFor(t, map[string]any{
		"command":            ".venv/bin/python3 rate_service.py",
		"timeout_ms":         120000,
		"capability_request": capReq(declared),
	}, declared)
	if !base.Complete() {
		t.Fatal("base lease is incomplete")
	}

	captured := directIPLeaseFor(t, map[string]any{
		"command":    ".venv/bin/python3 rate_service.py",
		"timeout_ms": 120000,
		"terminal_capture": map[string]any{
			"caption": "Exchange rate cache service demo",
			"winsize": map[string]any{"cols": 120, "rows": 40},
		},
		"capability_request": capReq(declared),
	}, declared)
	if captured.IdentityKey() != base.IdentityKey() {
		t.Fatal("a captured-terminal frame minted a second direct-network lease")
	}

	reordered := []string{"https://api.frankfurter.dev:443", "https://open.er-api.com:443"}
	shuffled := directIPLeaseFor(t, map[string]any{
		"command":            ".venv/bin/python3 rate_service.py",
		"timeout_ms":         120000,
		"capability_request": capReq(reordered),
	}, reordered)
	if shuffled.IdentityKey() != base.IdentityKey() {
		t.Fatal("the order the model listed its destinations in minted a second lease")
	}

	widened := directIPLeaseFor(t, map[string]any{
		"command":            ".venv/bin/python3 rate_service.py",
		"timeout_ms":         120000,
		"capability_request": map[string]any{"direct_ip": true},
	}, nil)
	if widened.IdentityKey() == base.IdentityKey() {
		t.Fatal("dropping the declaration rode the narrowed lease")
	}
	other := directIPLeaseFor(t, map[string]any{
		"command":            "rm -f rates.db && .venv/bin/python3 rate_service.py",
		"timeout_ms":         120000,
		"capability_request": capReq(declared),
	}, declared)
	if other.IdentityKey() == base.IdentityKey() {
		t.Fatal("a different command rode the lease")
	}
}
