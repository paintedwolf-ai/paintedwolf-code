package grounding_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/grounding"
)

func TestApplyUngroundedWarning_escalates(t *testing.T) {
	var state grounding.UngroundedCounter
	grounding.ApplyUngroundedWarning(&state, 3, 2, true)
	grounding.ApplyUngroundedWarning(&state, 3, 2, true)
	if !state.Escalated {
		t.Fatal("expected escalation after consecutive warnings")
	}
}

func TestResetUngroundedStreak_clearsConsecutive(t *testing.T) {
	state := grounding.UngroundedCounter{TotalWarnings: 2, ConsecutiveWarnings: 2}
	grounding.ResetUngroundedStreak(&state)
	if state.ConsecutiveWarnings != 0 {
		t.Fatalf("ConsecutiveWarnings = %d, want 0", state.ConsecutiveWarnings)
	}
	if state.TotalWarnings != 2 {
		t.Fatalf("TotalWarnings = %d, want 2 (unchanged)", state.TotalWarnings)
	}
}

func TestStateStore_roundtrip(t *testing.T) {
	s := grounding.NewStateStore()
	s.Set("sess", grounding.UngroundedCounter{Escalated: true})
	if !s.IsEscalated("sess") {
		t.Fatal("expected escalated")
	}
	s.Reset("sess")
	if s.IsEscalated("sess") {
		t.Fatal("expected cleared after reset")
	}
}
