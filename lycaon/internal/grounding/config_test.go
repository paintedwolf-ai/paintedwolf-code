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

// The write and ambient modes fall back to the post-turn mode until set.
func TestGroundingModesFallBackToThePostTurnMode(t *testing.T) {
	var cfg grounding.GroundingConfig
	cfg.PostTurn.Mode = "warn"
	if write, ambient := cfg.WriteGroundingMode(), cfg.AmbientPostTurnMode(); write != "warn" || ambient != "warn" {
		t.Fatalf("modes = %q, %q, want the post-turn mode for both", write, ambient)
	}
	cfg.Write.Mode, cfg.Ambient.PostTurn.Mode = "block", "audit"
	if write, ambient := cfg.WriteGroundingMode(), cfg.AmbientPostTurnMode(); write != "block" || ambient != "audit" {
		t.Fatalf("modes = %q, %q, want block and audit", write, ambient)
	}
}

// Each verdict code sets the grounding facts its rule decides on.
func TestObservedVerdictSetsTheFactsItsRuleReads(t *testing.T) {
	type facts struct {
		escalated, claims, ledgerJob, auditUngrounded bool
		surface                                       string
	}
	for _, tc := range []struct {
		code string
		want facts
	}{
		{grounding.CoordinatorEscalatedCode, facts{escalated: true, claims: true, ledgerJob: true}},
		{grounding.AmbientGroundingEscalatedCode, facts{escalated: true, ledgerJob: true}},
		{grounding.CoordinatorUngroundedClaimCode, facts{claims: true}},
		{grounding.CoordinatorCriteriaUnmetCode, facts{claims: true, ledgerJob: true}},
		{grounding.AmbientUngroundedCompletionCode, facts{auditUngrounded: true}},
		{guidance.InvestHandleNotObservedCode, facts{auditUngrounded: true, ledgerJob: true, surface: "implement_investigate"}},
		{guidance.SynthHandleNotInLegsCode, facts{auditUngrounded: true, ledgerJob: true, surface: "implement_dispatch"}},
	} {
		gc := &oar.GuardContext{}
		// The ledger fact starts set so a verdict that clears it is seen.
		gc.Grounding.HasMatchingLedgerJob = true
		grounding.ObserveVerdict(gc, grounding.GroundingVerdict{Code: tc.code, LegID: "leg-1"})
		got := facts{
			escalated: gc.Grounding.GroundingEscalated, claims: gc.Grounding.ClaimsCompletion,
			ledgerJob: gc.Grounding.HasMatchingLedgerJob, auditUngrounded: gc.Grounding.LastAuditUngrounded,
			surface: gc.Session.Surface,
		}
		if got != tc.want {
			t.Fatalf("%s set %+v, want %+v", tc.code, got, tc.want)
		}
		if data := gc.RejectData[tc.code]; data["grounding_verdict"] != tc.code || data["leg_id"] != "leg-1" {
			t.Fatalf("%s reject data = %v", tc.code, data)
		}
	}

	settled := &oar.GuardContext{}
	grounding.ObserveVerdict(settled, grounding.GroundingVerdict{OK: true, Code: grounding.CoordinatorEscalatedCode})
	if settled.Grounding.GroundingEscalated || len(settled.RejectData) != 0 {
		t.Fatalf("a passing verdict set facts: %+v", settled.Grounding)
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
