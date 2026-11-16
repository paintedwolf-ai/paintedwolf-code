package grounding_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
)

func TestCircuitBreakerEscalatesInBlockMode(t *testing.T) {
	cfg := grounding.DefaultGroundingConfig()
	cfg.CircuitBreaker.MaxConsecutiveUngrounded = 2
	cfg.CircuitBreaker.EscalateMode = "block"
	state := grounding.UngroundedCounter{}
	verdict := grounding.GroundingVerdict{OK: false, Code: grounding.CoordinatorUngroundedClaimCode}
	grounding.ApplyCircuitBreaker(&state, verdict, cfg)
	grounding.ApplyCircuitBreaker(&state, verdict, cfg)
	if !state.Escalated {
		t.Fatal("expected escalation after consecutive warnings")
	}
	grounding.ApplyCircuitBreaker(&state, grounding.GroundingVerdict{OK: true}, cfg)
	if state.ConsecutiveWarnings != 0 {
		t.Fatalf("ConsecutiveWarnings = %d, want 0 after OK verdict", state.ConsecutiveWarnings)
	}
}

func TestConfigModesAndObserveVerdict(t *testing.T) {
	var cfg grounding.GroundingConfig
	cfg.PostTurn.Mode = "warn"
	if got := cfg.WriteGroundingMode(); got != "warn" {
		t.Fatalf("WriteGroundingMode fallback = %q, want warn", got)
	}
	if got := cfg.AmbientPostTurnMode(); got != "warn" {
		t.Fatalf("AmbientPostTurnMode fallback = %q, want warn", got)
	}
	cfg.Write.Mode = "block"
	cfg.Ambient.PostTurn.Mode = "audit"
	if got := cfg.WriteGroundingMode(); got != "block" {
		t.Fatalf("WriteGroundingMode = %q, want block", got)
	}
	if got := cfg.AmbientPostTurnMode(); got != "audit" {
		t.Fatalf("AmbientPostTurnMode = %q, want audit", got)
	}

	grounding.ObserveVerdict(nil, grounding.GroundingVerdict{Code: grounding.CoordinatorEscalatedCode})
	gc := &oar.GuardContext{}
	grounding.ObserveVerdict(gc, grounding.GroundingVerdict{OK: true, Code: grounding.CoordinatorEscalatedCode})
	grounding.ObserveVerdict(gc, grounding.GroundingVerdict{})

	for _, tc := range []struct {
		code    string
		surface string
	}{
		{code: grounding.CoordinatorEscalatedCode},
		{code: grounding.AmbientGroundingEscalatedCode},
		{code: grounding.CoordinatorUngroundedClaimCode},
		{code: grounding.CoordinatorCriteriaUnmetCode},
		{code: grounding.AmbientUngroundedCompletionCode},
		{code: guidance.InvestHandleNotObservedCode, surface: "implement_investigate"},
		{code: guidance.SynthHandleNotInLegsCode, surface: "implement_dispatch"},
	} {
		ctx := &oar.GuardContext{}
		grounding.ObserveVerdict(ctx, grounding.GroundingVerdict{
			OK:     false,
			Code:   tc.code,
			Reason: "test reason",
			LegID:  "leg-1",
		})
		if tc.surface != "" && ctx.Session.Surface != tc.surface {
			t.Fatalf("code %s surface = %q, want %q", tc.code, ctx.Session.Surface, tc.surface)
		}
	}
}

func TestDefaultConfigFlagsWithoutHardBlock(t *testing.T) {
	cfg := grounding.DefaultGroundingConfig()
	state := grounding.UngroundedCounter{}
	verdict := grounding.GroundingVerdict{OK: false, Code: grounding.AmbientUngroundedCompletionCode}
	for range 10 {
		grounding.ApplyCircuitBreaker(&state, verdict, cfg)
	}
	if state.Escalated {
		t.Fatal("default escalate mode must not hard-block the session")
	}
	if !grounding.Flagged(state, cfg) {
		t.Fatal("accumulated warnings past threshold must raise an advisory flag")
	}
}
