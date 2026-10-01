package llm

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

func testCapacityPolicy() CapacityPolicy {
	return CapacityPolicy{
		HoldMax:       2,
		HoldWaitMs:    []int{1, 1},
		HoldMaxWaitMs: 10,
		CooldownMs:    5,
	}
}

func TestValidateCapacityPolicy(t *testing.T) {
	if err := ValidateCapacityPolicy(DefaultCapacityPolicy()); err != nil {
		t.Fatalf("default: %v", err)
	}
	disabled := CapacityPolicy{HoldMax: 0, CooldownMs: 30}
	if err := ValidateCapacityPolicy(disabled); err != nil {
		t.Fatalf("holds disabled: %v", err)
	}
	short := DefaultCapacityPolicy()
	short.HoldWaitMs = []int{1000}
	if err := ValidateCapacityPolicy(short); err == nil {
		t.Fatal("expected wait_ms length error")
	}
}

func TestProviderCapacityConfigDefault(t *testing.T) {
	var zero ProviderCapacityConfig
	p, err := zero.Policy()
	if err != nil {
		t.Fatalf("zero policy: %v", err)
	}
	want := DefaultCapacityPolicy()
	if p.HoldMax != want.HoldMax || p.CooldownMs != want.CooldownMs {
		t.Fatalf("zero policy = %+v want default %+v", p, want)
	}
}

func TestCapacityGateNilIsNoop(t *testing.T) {
	var g *CapacityGate
	if err := g.Await(context.Background(), "p", "m"); err != nil {
		t.Fatalf("Await: %v", err)
	}
	if g.ShouldHold(&failure.ProviderOverloadedError{Status: 503}, 0) {
		t.Fatal("nil gate must not hold")
	}
	g.NoteFailure("p", "m", &failure.ProviderOverloadedError{Status: 503})
	g.NoteSuccess("p", "m")
	g.Reset()
}

// A slot that cannot serve holds the turn, whichever way it failed to serve.
// A 429 is the provider answering — the request was wrong for right now, not
// the slot being down — so it stays out.
func TestCapacityGateHoldsEverySlotThatCannotServe(t *testing.T) {
	g := NewCapacityGate(testCapacityPolicy())
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"rate limited", &failure.ProviderRateLimitedError{Status: 429}, false},
		{"overloaded", &failure.ProviderOverloadedError{Status: 503}, true},
		{"silent", &failure.ProviderSilentError{ProviderID: "p", Silence: time.Minute}, true},
		{"unreachable", &failure.ProviderUnreachableError{ProviderID: "p"}, true},
		{"empty completion", &failure.ProviderEmptyCompletionError{ProviderID: "p"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.ShouldHold(tc.err, 0); got != tc.want {
				t.Fatalf("ShouldHold(%T) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Silence rests a slot longer than an overload: it costs the whole header bound
// before anything is learned, so reissuing into it immediately spends that again.
func TestCapacityGateCoolsSilenceLongerThanOverload(t *testing.T) {
	policy := testCapacityPolicy()
	policy.CooldownMs = 5
	policy.SilentMs = 60
	g := NewCapacityGate(policy)

	g.NoteFailure("p", "m", &failure.ProviderSilentError{ProviderID: "p"})
	g.mu.Lock()
	silentUntil, okSilent := g.slots[capacitySlotKey("p", "m")]
	g.mu.Unlock()
	if !okSilent {
		t.Fatal("a silent provider must cool its slot")
	}

	g2 := NewCapacityGate(policy)
	g2.NoteFailure("p", "m", &failure.ProviderOverloadedError{Status: 503})
	g2.mu.Lock()
	overUntil, okOver := g2.slots[capacitySlotKey("p", "m")]
	g2.mu.Unlock()
	if !okOver {
		t.Fatal("an overloaded provider must cool its slot")
	}
	if !silentUntil.After(overUntil) {
		t.Fatalf("silence cooldown %v must outlast the overload cooldown %v", silentUntil, overUntil)
	}
}

// An absent silent_ms reuses the overload schedule rather than disabling the cooldown.
func TestCapacityCooldownDefaultsSilenceToTheOverloadSchedule(t *testing.T) {
	cfg := ProviderCapacityConfig{
		Hold:     CapacityHoldConfig{MaxHolds: 1, WaitMs: []int{10}, MaxWaitMs: 10},
		Cooldown: CapacityCooldownConfig{OverloadedMs: 30000},
	}
	policy, err := cfg.Policy()
	if err != nil {
		t.Fatalf("Policy: %v", err)
	}
	if policy.SilentMs != 30000 {
		t.Fatalf("SilentMs = %d, want the overload cooldown 30000", policy.SilentMs)
	}
}

func TestCapacityGateHoldOnlyOverloaded(t *testing.T) {
	g := NewCapacityGate(testCapacityPolicy())
	if g.ShouldHold(&failure.ProviderRateLimitedError{Status: 429}, 0) {
		t.Fatal("429 must not hold")
	}
	if !g.ShouldHold(&failure.ProviderOverloadedError{Status: 503}, 0) {
		t.Fatal("503 should hold")
	}
	if !g.ShouldHold(&failure.ProviderOverloadedError{Status: 503}, 1) {
		t.Fatal("second hold should be allowed")
	}
	if g.ShouldHold(&failure.ProviderOverloadedError{Status: 503}, 2) {
		t.Fatal("hold budget is 2")
	}
}

func TestCapacityGateHoldSleeps(t *testing.T) {
	g := NewCapacityGate(testCapacityPolicy())
	var seen []providerretry.RetryAttempt
	ctx := providerretry.WithRetryObserver(context.Background(), func(a providerretry.RetryAttempt) {
		seen = append(seen, a)
	})
	start := time.Now()
	if err := g.Hold(ctx, 0); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if time.Since(start) < time.Millisecond {
		t.Fatal("Hold should sleep the scheduled wait")
	}
	if len(seen) != 1 || seen[0].Reason != providerretry.RetryReasonHold {
		t.Fatalf("observed %+v want one hold", seen)
	}
}

func TestCapacityGateCooldown(t *testing.T) {
	g := NewCapacityGate(testCapacityPolicy())
	g.NoteFailure("together", "gpt-oss", &failure.ProviderRateLimitedError{Status: 429})
	if err := g.Await(context.Background(), "together", "gpt-oss"); err != nil {
		t.Fatalf("429 must not open a cooldown: %v", err)
	}

	var seen []providerretry.RetryAttempt
	ctx := providerretry.WithRetryObserver(context.Background(), func(a providerretry.RetryAttempt) {
		seen = append(seen, a)
	})
	g.NoteFailure("together", "gpt-oss", &failure.ProviderOverloadedError{Status: 503})
	start := time.Now()
	if err := g.Await(ctx, "together", "gpt-oss"); err != nil {
		t.Fatalf("Await: %v", err)
	}
	if time.Since(start) < time.Millisecond {
		t.Fatal("Await should sleep the remaining cooldown")
	}
	if len(seen) != 1 || seen[0].Reason != providerretry.RetryReasonCooldown {
		t.Fatalf("observed %+v want one cooldown", seen)
	}

	g.NoteFailure("together", "gpt-oss", &failure.ProviderOverloadedError{Status: 503})
	g.NoteSuccess("together", "gpt-oss")
	if err := g.Await(context.Background(), "together", "gpt-oss"); err != nil {
		t.Fatalf("success must clear cooldown: %v", err)
	}
}

func TestCapacityGateResetClearsSlots(t *testing.T) {
	g := NewCapacityGate(testCapacityPolicy())
	g.NoteFailure("p", "m", &failure.ProviderOverloadedError{Status: 503})
	g.Reset()
	if err := g.Await(context.Background(), "p", "m"); err != nil {
		t.Fatalf("Reset must clear cooldown: %v", err)
	}
}

func TestExhaustedHTTPError(t *testing.T) {
	if _, ok := failure.AsProviderRateLimited(providerretry.ExhaustedHTTPError("p", "m", 429, 3, nil)); !ok {
		t.Fatal("429 must be rate limited")
	}
	if _, ok := failure.AsProviderOverloaded(providerretry.ExhaustedHTTPError("p", "m", 503, 3, nil)); !ok {
		t.Fatal("503 must be overloaded")
	}
	if _, ok := failure.AsProviderOverloaded(providerretry.ExhaustedHTTPError("p", "m", 529, 3, nil)); !ok {
		t.Fatal("529 must be overloaded")
	}
	server, ok := failure.AsProviderServer(providerretry.ExhaustedHTTPError("p", "m", 502, 3, nil))
	if !ok {
		t.Fatal("502 must be a provider server error")
	}
	if server.ProviderID != "p" || server.Model != "m" || server.Status != 502 || server.Attempts != 3 {
		t.Fatalf("server error = %+v", server)
	}
}
