package grounding_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/session/closeouts"
)

// TestGB02InProcessControls verifies D1's real evidence resolver, shared
// closeout friction counter (D10), and D8 in-process block-mode circuit breaker
// via a test-only Go overlay (zero edits to any shipped D1 file or binary).
func TestGB02InProcessControls(t *testing.T) {
	t.Run("citation_verdicts_supported_unsupported_and_classes", func(t *testing.T) {
		ev := evidence.Ledger{
			Handles: map[string]evidence.Record{
				"read#1": {
					Handle:     "read#1",
					Kind:       "read",
					Shape:      evidence.ShapeFileRegion,
					Path:       "sample.go",
					LineRanges: []evidence.LineRange{{Start: 1, End: 3}},
					Body: []string{
						"1\tpackage sample",
						"2\t",
						"3\tfunc Hello() string { return \"ok\" }",
					},
				},
			},
			ByPath: map[string][]string{
				"sample.go": {"read#1"},
			},
		}
		roots := evidence.CitationRoots{ProjectDir: "/tmp/gb02-inprocess"}

		// Supported citation with verified excerpt -> VerdictMatched (grounded)
		resMatched := evidence.Resolve(
			roots,
			evidence.Triple{Path: "sample.go", Line: 3, Excerpt: `return "ok"`},
			ev,
			"read#1",
		)
		if !resMatched.Verdict.Grounded() || resMatched.Handle != "read#1" {
			t.Fatalf("supported citation expected grounded read#1, got verdict=%q handle=%q", resMatched.Verdict, resMatched.Handle)
		}

		// Supported coordinate without excerpt -> VerdictTraced (grounded)
		resTraced := evidence.Resolve(
			roots,
			evidence.Triple{Path: "sample.go", Line: 3},
			ev,
			"read#1",
		)
		if resTraced.Verdict != evidence.VerdictTraced || !resTraced.Verdict.Grounded() {
			t.Fatalf("coordinate-only citation expected traced, got verdict=%q", resTraced.Verdict)
		}

		// Unsupported path -> VerdictUnverifiable (ungrounded)
		resUnsup := evidence.Resolve(
			roots,
			evidence.Triple{Path: "unobserved.go", Line: 1},
			ev,
			"",
		)
		if resUnsup.Verdict != evidence.VerdictUnverifiable || resUnsup.Verdict.Grounded() {
			t.Fatalf("unobserved path expected unverifiable, got verdict=%q", resUnsup.Verdict)
		}

		// Verify D4 grounded vs ungrounded class boundaries on all 5 verdicts
		for _, v := range []evidence.Verdict{evidence.VerdictMatched, evidence.VerdictTraced, evidence.VerdictBound} {
			if !v.Grounded() {
				t.Fatalf("expected %q to be Grounded()", v)
			}
		}
		for _, v := range []evidence.Verdict{evidence.VerdictAmbiguous, evidence.VerdictUnverifiable} {
			if v.Grounded() {
				t.Fatalf("expected %q NOT to be Grounded()", v)
			}
		}
	})

	t.Run("d10_exact_shared_friction_counter", func(t *testing.T) {
		svc := closeouts.New(nil)
		ctx := context.Background()
		sessionID := "gb02-friction-session"

		// First charge decrements remaining by exactly 1 from the turn/cycle cap
		first := svc.RecordGroundingFriction(ctx, sessionID)
		wantFirst := min(limits.DefaultGroundingRejectsPerTurn-1, limits.DefaultGroundingRejectsPerCycle-1)
		if first.Remaining != wantFirst {
			t.Fatalf("after 1 charge: remaining=%d, want=%d", first.Remaining, wantFirst)
		}

		// Second charge decrements remaining by another 1
		second := svc.RecordGroundingFriction(ctx, sessionID)
		if second.Remaining != first.Remaining-1 {
			t.Fatalf("after 2 charges: remaining=%d, want=%d", second.Remaining, first.Remaining-1)
		}
	})

	t.Run("d8_circuit_breaker_block_mode_thresholds", func(t *testing.T) {
		store := grounding.NewStateStore()

		// Threshold 1: 2 consecutive ungrounded turns escalate in block mode
		sConsec := store.Get("sess-consec")
		grounding.ApplyUngroundedWarning(&sConsec, 3, 2, true)
		if sConsec.Escalated {
			t.Fatal("should not escalate after 1 warning")
		}
		grounding.ApplyUngroundedWarning(&sConsec, 3, 2, true)
		store.Set("sess-consec", sConsec)
		if !store.IsEscalated("sess-consec") {
			t.Fatal("expected escalation at 2 consecutive warnings in block mode")
		}

		// Threshold 2: 3 total non-consecutive warnings escalate in block mode
		sTotal := store.Get("sess-total")
		grounding.ApplyUngroundedWarning(&sTotal, 3, 2, true)
		grounding.ResetUngroundedStreak(&sTotal)
		grounding.ApplyUngroundedWarning(&sTotal, 3, 2, true)
		grounding.ResetUngroundedStreak(&sTotal)
		if sTotal.Escalated {
			t.Fatal("should not escalate after 2 non-consecutive warnings")
		}
		grounding.ApplyUngroundedWarning(&sTotal, 3, 2, true)
		store.Set("sess-total", sTotal)
		if !store.IsEscalated("sess-total") {
			t.Fatal("expected escalation at 3 total warnings in block mode")
		}
	})
}
