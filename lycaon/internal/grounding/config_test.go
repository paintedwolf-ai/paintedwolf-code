package grounding_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/grounding"
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
